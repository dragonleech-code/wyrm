package main

// Verbs that show running sessions: the interactive picker, the full-screen
// TUI, and the non-interactive list the other two exist to replace for scripts.

import (
	"encoding/json"
	"fmt"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tui"
	"github.com/pelletier/go-toml/v2"
)

// list prints the running tmux sessions non-interactively, in the given
// format — for scripts and status bars, where the interactive picker doesn't
// apply. An empty session list is not an error in any format: table mode
// reports it on stderr but exits 0; json/toml print an empty array so
// consumers don't need to special-case "no server running".
func (a *app) list(args []string) error {
	fs := a.newFlagSet("list")
	format := fs.String("format", "table", "output format: table, json, toml, or names")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	running, err := sessions.List(a.runner)
	if err != nil {
		return err
	}
	if running == nil {
		running = []sessions.Session{}
	}

	switch *format {
	case "table":
		if len(running) == 0 {
			_, _ = fmt.Fprintln(a.stderr, "wyrm: no running tmux sessions")
			return nil
		}
		for _, s := range running {
			_, _ = fmt.Fprintln(a.stdout, formatSessionRow(s))
		}
	case "names":
		for _, s := range running {
			_, _ = fmt.Fprintln(a.stdout, s.Name)
		}
	case "json":
		data, err := json.MarshalIndent(running, "", "  ")
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(a.stdout, string(data))
	case "toml":
		data, err := toml.Marshal(struct {
			Sessions []sessions.Session `toml:"sessions"`
		}{Sessions: running})
		if err != nil {
			return err
		}
		_, _ = a.stdout.Write(data)
	default:
		// Exit 2, like any other bad flag value — an unknown -format is a usage
		// error, not a runtime failure.
		return usageErrf("unknown -format %q (use table, json, toml, or names)", *format)
	}
	return nil
}

// formatSessionRow renders one session as a plain, awk-able line: name,
// window count, and an attached marker — the same shape as the picker's row,
// minus color codes. See sessions.FormatRow.
func formatSessionRow(s sessions.Session) string {
	return sessions.FormatRow(s)
}

// pick lets the user choose a running session and attaches to it. An empty
// choice (nothing running, or the user aborted) exits quietly.
//
// It runs the same program `wyrm tui` does, in its compact two-panel form with
// the filter already open — see tui.RunPicker — so the two share every key and
// action.
func (a *app) pick(args []string) error {
	fs := a.newFlagSet("pick")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	// Check before taking the screen. With nothing running there is nothing to
	// pick, and a full-screen program that opens onto an empty list and has to
	// be quit again is a worse answer than one line on stderr. It also keeps
	// `wyrm pick` usable where there is no TTY at all, e.g. in a script.
	running, err := sessions.List(a.runner)
	if err != nil {
		return err
	}
	if len(running) == 0 {
		_, _ = fmt.Fprintln(a.stderr, "wyrm: no running tmux sessions")
		return nil
	}

	sessionID, err := tui.RunPicker(a.runner, settings, a.stderr)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return nil
	}
	return a.attachOrSwitch(sessionID)
}

// tui opens the interactive session-management TUI and, if the user chose a
// session to attach to, hands the terminal over after the alt-screen program
// exits — the same deferred-attach dance pick uses, since a full-screen
// program can't attach in place.
func (a *app) tui(args []string) error {
	fs := a.newFlagSet("tui")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	sessionID, err := tui.Run(a.runner, settings, a.stderr)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return nil
	}
	return a.attachOrSwitch(sessionID)
}
