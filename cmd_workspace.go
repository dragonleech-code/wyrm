package main

// Workspaces: `wyrm @name` starts every project in a [[workspace]] from the
// settings file and attaches to the first; `wyrm kill @name` stops them all.

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/state"
)

// workspaceProjects resolves a workspace's members to projects. A member
// that matches no project fails the whole command before anything starts, so
// a typo in the settings file can't leave half a workspace running.
func workspaceProjects(settings *config.Settings, name string) ([]config.Project, error) {
	ws, ok := settings.FindWorkspace(name)
	if !ok {
		var names []string
		for _, w := range settings.Workspaces {
			names = append(names, "@"+w.Name)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("no workspace named %q (define one with [[workspace]] in the settings file)", name)
		}
		return nil, fmt.Errorf("no workspace named %q (have: %s)", name, strings.Join(names, ", "))
	}
	if len(ws.Projects) == 0 {
		return nil, fmt.Errorf("workspace %q lists no projects", name)
	}
	index := config.NewProjectIndex(settings)
	projects := make([]config.Project, 0, len(ws.Projects))
	for _, member := range ws.Projects {
		p, ok := index.Find(member)
		if !ok {
			return nil, fmt.Errorf("workspace %q: no known project named %q", name, member)
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// upWorkspace builds every project in the workspace that isn't already
// running, then attaches to the first. A project that fails to build is
// reported and the rest still start, matching `restart -all`.
func (a *app) upWorkspace(name string, args []string) error {
	fs := a.newFlagSet("@" + name)
	detach := fs.Bool("d", false, "start the workspace without attaching")
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
	projects, err := workspaceProjects(settings, name)
	if err != nil {
		return err
	}
	hist, err := state.Load()
	if err != nil {
		return err
	}

	var failures []error
	var firstCfg *config.Config
	firstID := ""
	for _, p := range projects {
		cfg, err := p.LoadConfig()
		if err == nil {
			a.printWarnings(cfg)
			err = a.ensureTrusted(settings, cfg)
		}
		if err != nil {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: skipping %s: %v\n", p.Name, err)
			failures = append(failures, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		sessionName, id, created, err := session.Create(a.runner, cfg, a.stdout, a.stderr, session.WithHistory(hist))
		if err != nil {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: failed to start %s: %v\n", p.Name, err)
			failures = append(failures, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		if created {
			_, _ = fmt.Fprintf(a.stdout, "created session %s\n", sessionName)
		} else {
			_, _ = fmt.Fprintf(a.stdout, "session %s already running\n", sessionName)
		}
		if firstID == "" {
			firstCfg, firstID = cfg, id
		}
	}
	if firstID != "" && !*detach {
		if err := a.attachSession(settings, firstCfg, firstID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// killWorkspace stops every running project in the workspace, running each
// one's on_project_exit. A member that isn't running is skipped quietly:
// stopping a workspace means "none of these are running afterwards".
func (a *app) killWorkspace(settings *config.Settings, name string, dryRun bool) error {
	projects, err := workspaceProjects(settings, name)
	if err != nil {
		return err
	}
	var base []session.Option
	if dryRun {
		base = a.teardownDryRun()
	}
	var failures []error
	for _, p := range projects {
		cfg, err := p.LoadConfig()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		opts, err := a.killOptions(settings, cfg, slices.Clone(base), dryRun)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		sessionName, err := session.Kill(a.runner, cfg, a.stderr, opts...)
		switch {
		case errors.Is(err, session.ErrSessionNotRunning):
		case err != nil:
			failures = append(failures, fmt.Errorf("%s: %w", p.Name, err))
		case !dryRun:
			_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", sessionName)
		}
	}
	return errors.Join(failures...)
}
