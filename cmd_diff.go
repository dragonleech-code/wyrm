package main

// `wyrm diff` (how a running session has drifted from its config) and
// `wyrm wait` (the readiness check behind a split's wait_for).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/wait"
)

// diff compares the config with its running session, window by window. Like
// diff(1) it exits 1 when there are differences, so a script can ask "is this
// session what the config says?".
func (a *app) diff(args []string) error {
	fs := a.newFlagSet("diff")
	configPath := fs.String("config", "", "path to config file (default: .wyrm.toml, then .tmuxconfig)")
	format := fs.String("format", "text", "output format: text or json")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usageErrf("diff takes at most one project name")
	}
	if *format != "text" && *format != "json" {
		return usageErrf("unknown format %q (want text or json)", *format)
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	var cfg *config.Config
	if name := fs.Arg(0); name != "" {
		if *configPath != "" {
			return usageErrf("-config and a project name are mutually exclusive")
		}
		project, ok := config.FindProject(settings, name)
		if !ok {
			return fmt.Errorf("no known project named %q", name)
		}
		if cfg, err = project.LoadConfig(); err != nil {
			return err
		}
	} else if cfg, _, err = a.resolveConfig(settings, *configPath); err != nil {
		return err
	}

	name, _, drift, err := session.Diff(a.runner, cfg)
	if err != nil {
		return err
	}
	if *format == "json" {
		out := struct {
			Session string                `json:"session"`
			Drift   []session.WindowDrift `json:"drift"`
		}{name, drift}
		if out.Drift == nil {
			out.Drift = []session.WindowDrift{}
		}
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		printDrift(a, name, drift)
	}
	if len(drift) > 0 {
		return silent(1)
	}
	return nil
}

func printDrift(a *app, name string, drift []session.WindowDrift) {
	if len(drift) == 0 {
		_, _ = fmt.Fprintf(a.stdout, "session %s matches its config\n", name)
		return
	}
	_, _ = fmt.Fprintf(a.stdout, "session %s differs from its config:\n", name)
	for _, d := range drift {
		switch d.Kind {
		case session.DriftMissing:
			_, _ = fmt.Fprintf(a.stdout, "  + window %q is in the config but not running\n", d.Window)
		case session.DriftExtra:
			_, _ = fmt.Fprintf(a.stdout, "  - window %q is running but not in the config\n", d.Window)
		case session.DriftPanes:
			_, _ = fmt.Fprintf(a.stdout, "  ~ window %q has %d pane(s); the config has %d\n", d.Window, d.LivePanes, d.ConfigPanes)
		}
	}
	_, _ = fmt.Fprintln(a.stdout, "run `wyrm up -reconcile` to add missing windows, or `wyrm restart` to rebuild")
}

// waitCmd blocks until every spec is ready. A split's wait_for runs it ahead
// of the pane's command, joined with &&, so a timeout keeps the command from
// starting.
func (a *app) waitCmd(args []string) error {
	fs := a.newFlagSet("wait")
	timeout := fs.Duration("timeout", wait.DefaultTimeout, "give up after this long")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return usageErrf("wyrm wait needs at least one target: tcp:HOST:PORT, file:PATH, or an http(s) URL")
	}
	if *timeout <= 0 {
		return usageErrf("-timeout must be positive, got %s", *timeout)
	}
	targets := make([]wait.Target, 0, fs.NArg())
	for _, spec := range fs.Args() {
		t, err := wait.Parse(spec)
		if err != nil {
			return usageErrf("%v", err)
		}
		targets = append(targets, t)
	}
	ctx := a.watchCtx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	start := time.Now()
	_, _ = fmt.Fprintf(a.stderr, "wyrm: waiting for %s\n", joinTargets(targets))
	if err := wait.For(ctx, targets); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return silent(1)
		}
		return err
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: ready after %s\n", time.Since(start).Round(100*time.Millisecond))
	return nil
}

func joinTargets(ts []wait.Target) string {
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += ", "
		}
		out += t.String()
	}
	return out
}
