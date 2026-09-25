package main

// Subcommands that inspect running agent status across sessions: status, agent-status.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

type agentStatusPane struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	WindowID    string `json:"window_id"`
	WindowName  string `json:"window_name"`
	WindowIndex int    `json:"window_index"`
	PaneID      string `json:"pane_id"`
	PaneIndex   int    `json:"pane_index"`
	Command     string `json:"command"`
	State       string `json:"state"`
}

type agentStatusSummary struct {
	Total   int `json:"total"`
	Blocked int `json:"blocked"`
	Idle    int `json:"idle"`
	Busy    int `json:"busy"`
	// Skipped counts agent panes found but not captured, because the scan is
	// bounded at agent.MaxCaptures. Reported rather than dropped: a status
	// line that quietly stops counting at 16 is worse than one that says it
	// stopped, especially under --watch where it is the only thing on screen.
	Skipped int `json:"skipped,omitempty"`
}

type agentStatusReport struct {
	Summary agentStatusSummary `json:"summary"`
	Agents  []agentStatusPane  `json:"agents"`
}

func (a *app) status(args []string) error {
	fs := a.newFlagSet("status")
	format := fs.String("format", "text", "output format: text, json, tmux, waybar, sketchybar")
	sessionFilter := fs.String("session", "", "filter to a specific session by name or ID")
	verbose := fs.Bool("v", false, "verbose text output")
	watchLong := fs.Bool("watch", false, "continuously stream status output")
	watchShort := fs.Bool("w", false, "alias for -watch")
	interval := fs.Duration("interval", 2*time.Second, "polling interval in watch mode")

	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	watch := *watchLong || *watchShort
	// time.NewTicker panics on a non-positive duration, so a plain typo
	// (`-interval 0`) used to reach the user as a stack trace. Exit 2, like
	// every other bad flag value.
	if watch && *interval <= 0 {
		return usageErrf("-interval must be positive, got %s", *interval)
	}

	// The error was previously discarded here, so a malformed settings file
	// silently produced "no active agents" with no explanation.
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	profiles, err := agent.ProfilesFrom(settings)
	if err != nil {
		return err
	}

	if !watch {
		report, err := collectStatus(a.runner, *sessionFilter, profiles)
		if err != nil {
			return err
		}
		return formatStatus(a.stdout, *format, *verbose, report)
	}

	ctx := a.watchCtx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
	}

	// Collect and format once up front, outside the loop, so a bad -format
	// (or any other collection failure) fails the way it does without
	// -watch — the loop below used to swallow every iteration's error
	// silently, so a typo'd -format looped forever printing nothing at all,
	// with a zero exit code, until interrupted.
	report, err := collectStatus(a.runner, *sessionFilter, profiles)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := formatStatus(&buf, *format, *verbose, report); err != nil {
		return err
	}
	lastOut := buf.String()
	_, _ = fmt.Fprint(a.stdout, lastOut)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	// lastErr dedupes a repeating failure so a downed tmux server doesn't
	// spam a warning every tick — only a *new* error (or recovery) is worth
	// telling the user about.
	lastErr := ""
	warnOnce := func(context string, err error) {
		if msg := err.Error(); msg != lastErr {
			lastErr = msg
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: %s: %v\n", context, err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		report, err := collectStatus(a.runner, *sessionFilter, profiles)
		if err != nil {
			warnOnce("status collection failed", err)
			continue
		}
		buf.Reset()
		if err := formatStatus(&buf, *format, *verbose, report); err != nil {
			warnOnce("formatting status failed", err)
			continue
		}
		lastErr = ""
		out := buf.String()
		if out != lastOut {
			lastOut = out
			_, _ = fmt.Fprint(a.stdout, out)
		}
	}
}

func collectStatus(runner tmux.Runner, sessionFilter string, profiles []agent.Profile) (agentStatusReport, error) {
	var report agentStatusReport
	report.Agents = make([]agentStatusPane, 0)

	refs, err := tmux.ListAllPanes(runner)
	if err != nil {
		return report, err
	}

	// Filter to the requested session first, then let agent.Candidates apply
	// the same bound the TUI uses. Without it `wyrm status --watch` issued one
	// capture-pane per agent pane every interval, unbounded, against the
	// server the user is working in.
	var scoped []tmux.PaneRef
	for _, ref := range refs {
		if sessionFilter != "" && ref.SessionName != sessionFilter && ref.SessionID != sessionFilter {
			continue
		}
		scoped = append(scoped, ref)
	}
	detections, skipped := agent.Scan(runner, scoped, profiles, "", agent.MaxCaptures)
	report.Summary.Skipped = skipped

	for _, detection := range detections {
		ref, st := detection.Ref, detection.State
		report.Summary.Total++
		switch st {
		case agent.StateBlocked:
			report.Summary.Blocked++
		case agent.StateIdle:
			report.Summary.Idle++
		case agent.StateBusy:
			report.Summary.Busy++
		}
		report.Agents = append(report.Agents, agentStatusPane{
			SessionID:   ref.SessionID,
			SessionName: ref.SessionName,
			WindowID:    ref.WindowID,
			WindowName:  ref.WindowName,
			WindowIndex: ref.WindowIndex,
			PaneID:      ref.PaneID,
			PaneIndex:   ref.PaneIndex,
			Command:     ref.Command,
			State:       st.String(),
		})
	}

	return report, nil
}

func formatStatus(w io.Writer, format string, verbose bool, report agentStatusReport) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)

	case "tmux":
		var parts []string
		if report.Summary.Blocked > 0 {
			parts = append(parts, fmt.Sprintf("#[fg=yellow,bold]⏸ %d blocked#[default]", report.Summary.Blocked))
		}
		if report.Summary.Idle > 0 {
			parts = append(parts, fmt.Sprintf("#[fg=cyan]✓ %d idle#[default]", report.Summary.Idle))
		}
		// Not gated on parts being non-empty. Every scanned agent being busy
		// produces no parts at all, and that is exactly when an unscanned pane
		// matters most — one of the ones we did not read could be blocked.
		if report.Summary.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("#[fg=red]+%d unscanned#[default]", report.Summary.Skipped))
		}
		if len(parts) > 0 {
			_, _ = fmt.Fprintln(w, strings.Join(parts, " · "))
		}
		return nil

	case "waybar":
		type waybarOut struct {
			Text    string `json:"text"`
			Alt     string `json:"alt"`
			Tooltip string `json:"tooltip"`
			Class   string `json:"class"`
		}
		var wb waybarOut
		var parts []string
		var tooltips []string
		for _, ag := range report.Agents {
			tooltips = append(tooltips, fmt.Sprintf("%s: @%d:%s %s (%s)", ag.SessionName, ag.WindowIndex, ag.WindowName, ag.PaneID, ag.State))
		}
		if report.Summary.Blocked > 0 {
			parts = append(parts, fmt.Sprintf("⏸ %d", report.Summary.Blocked))
			wb.Class = "blocked"
			wb.Alt = "blocked"
		}
		if report.Summary.Idle > 0 {
			parts = append(parts, fmt.Sprintf("✓ %d", report.Summary.Idle))
			if wb.Class == "" {
				wb.Class = "idle"
				wb.Alt = "idle"
			}
		}
		if report.Summary.Skipped > 0 {
			tooltips = append(tooltips, fmt.Sprintf("%d further agent pane(s) not scanned (scan is capped at %d)",
				report.Summary.Skipped, agent.MaxCaptures))
		}
		if len(parts) == 0 {
			wb.Class = "none"
			wb.Alt = "none"
			wb.Tooltip = "No active agents"
		} else {
			wb.Text = strings.Join(parts, " · ")
			wb.Tooltip = strings.Join(tooltips, "\n")
		}
		return json.NewEncoder(w).Encode(wb)

	case "sketchybar":
		switch {
		case report.Summary.Blocked > 0:
			_, _ = fmt.Fprintf(w, "icon=⏸ label=\"%d blocked\" drawing=on\n", report.Summary.Blocked)
		case report.Summary.Idle > 0:
			_, _ = fmt.Fprintf(w, "icon=✓ label=\"%d idle\" drawing=on\n", report.Summary.Idle)
		default:
			_, _ = fmt.Fprintln(w, "drawing=off")
		}
		return nil

	case "text":
		if verbose {
			if len(report.Agents) == 0 {
				_, _ = fmt.Fprintln(w, "no active agents")
				return nil
			}
			for _, ag := range report.Agents {
				_, _ = fmt.Fprintf(w, "%s: @%d:%s %s (%s) - %s\n",
					ag.SessionName, ag.WindowIndex, ag.WindowName, ag.PaneID, ag.Command, ag.State)
			}
			if report.Summary.Skipped > 0 {
				_, _ = fmt.Fprintf(w, "%d further agent pane(s) not scanned (capped at %d)\n",
					report.Summary.Skipped, agent.MaxCaptures)
			}
			return nil
		}
		var parts []string
		if report.Summary.Blocked > 0 {
			parts = append(parts, fmt.Sprintf("⏸ %d blocked", report.Summary.Blocked))
		}
		if report.Summary.Idle > 0 {
			parts = append(parts, fmt.Sprintf("✓ %d idle", report.Summary.Idle))
		}
		// See the tmux case: reported even when nothing else is, since an
		// all-busy screen is precisely when a truncated scan could be hiding
		// the one pane that is waiting on you.
		if report.Summary.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("+%d unscanned", report.Summary.Skipped))
		}
		if len(parts) > 0 {
			_, _ = fmt.Fprintln(w, strings.Join(parts, " · "))
		}
		return nil

	default:
		return usageErrf("unknown format %q (want text, json, tmux, waybar, sketchybar)", format)
	}
}
