package main

// Verbs that build, attach to, and destroy sessions: up, restart, kill, and the
// bare-name form that resolves to either a running session or a known project.

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/state"
	"github.com/jskoll/wyrm/internal/tmux"
)

// killForRestart runs Kill as a restart's teardown half, printing a "killed"
// line on success. A session that isn't running is fine — restart means "end
// up with a freshly built session", satisfiable either way — but any other
// failure (a bad session.root, a tmux server/communication problem,
// kill-session itself failing) is real: the existing session most likely
// still exists, so building over it anyway and reporting "created" would
// misrepresent what actually happened. proceed reports whether it's safe to
// go on to Create.
func (a *app) killForRestart(cfg *config.Config) (proceed bool, err error) {
	if err := cfg.Validate(); err != nil {
		return false, fmt.Errorf("invalid replacement config: %w", err)
	}
	if err := session.ValidateRoots(cfg); err != nil {
		return false, fmt.Errorf("invalid replacement config: %w", err)
	}
	name, kerr := session.Kill(a.runner, cfg, a.stderr)
	if kerr == nil {
		_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", name)
		return true, nil
	}
	if errors.Is(kerr, session.ErrSessionNotRunning) {
		_, _ = fmt.Fprintf(a.stderr, "wyrm: nothing to stop (%v)\n", kerr)
		return true, nil
	}
	return false, kerr
}

// varMapFlag collects repeated --var KEY=VALUE flags into a map for template interpolation.
type varMapFlag map[string]string

func (v *varMapFlag) String() string {
	return ""
}

func (v *varMapFlag) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("invalid variable %q (expected KEY=VALUE)", s)
	}
	if *v == nil {
		*v = make(map[string]string)
	}
	(*v)[k] = val
	return nil
}

// up builds the current folder's session (or attaches if it's already
// running). This is the default when no subcommand is given.
func (a *app) up(args []string) error {
	fs := a.newFlagSet("up")
	configPath := fs.String("config", "", "path to config file (default: .wyrm.toml, then .tmuxconfig)")
	dryRun := fs.Bool("n", false, "print the tmux commands and hooks that would run, without touching tmux")
	detach := fs.Bool("d", false, "build the session without attaching")
	var vars varMapFlag
	fs.Var(&vars, "var", "set template variable (KEY=VALUE, can be repeated)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageErrf("unexpected argument %q (attach by name with `wyrm %s`, not `wyrm up %s`)",
			fs.Arg(0), fs.Arg(0), fs.Arg(0))
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	// ResolveEffective mirrors this exact discovery order — explicit path,
	// discovered local/shared file, user default, built-in default — and
	// session.Create reattaches instead of rebuilding when a session by that
	// name is already running, so unrelated sessions elsewhere don't matter.
	cfg, _, err := a.resolveConfig(settings, *configPath)
	if err != nil {
		return err
	}
	if len(vars) > 0 {
		cfg.Interpolate(vars)
	}

	hist, err := state.Load()
	if err != nil {
		return err
	}

	if *dryRun {
		return a.dryRunBuild(cfg, hist, !*detach)
	}

	name, sessionID, created, err := session.Create(a.runner, cfg, a.stdout, a.stderr, session.WithHistory(hist))
	if err != nil {
		return err
	}
	a.reportCreated(name, created)
	if *detach {
		_, _ = fmt.Fprintf(a.stdout, "run `wyrm %s` to attach\n", name)
		return nil
	}
	return a.attachSession(cfg, sessionID)
}

// attachSession runs on_project_attach and then hands the terminal over.
//
// The hook lives here rather than in session.Create so that it fires exactly
// when wyrm attaches and never when it doesn't: `up -d`, `restart -d` and
// `restart -all` all build a session and return without attaching, and Create
// fired the hook for all three.
func (a *app) attachSession(cfg *config.Config, sessionID string) error {
	_ = session.RunAttachHook(cfg, a.stderr)
	return a.attachOrSwitch(sessionID)
}

// reportCreated prints the one line distinguishing a fresh build from a
// reattach, which up, restart, and startProject all owe the user.
func (a *app) reportCreated(name string, created bool) {
	if created {
		_, _ = fmt.Fprintf(a.stdout, "created session %s\n", name)
		return
	}
	_, _ = fmt.Fprintf(a.stdout, "session %s already running, attaching\n", name)
}

// dryRunBuild prints the tmux commands `wyrm up` would issue, and the lifecycle
// hooks it would run, without doing either. A wyrm config executes arbitrary
// shell by design, so being able to read the plan before running it is worth
// the small amount of machinery — session.Create takes a tmux.Runner, so a
// recording one covers the tmux half, and session.DryRun covers the hooks,
// which never go through the Runner at all.
func (a *app) dryRunBuild(cfg *config.Config, hist session.HookHistory, attach bool) error {
	a.dryRunHeader(
		"dry run: no tmux commands are executed, no lifecycle",
		"hooks are run, and an already-running session is not",
		"consulted.")
	dry := tmux.NewDryRun(a.stdout)
	_, _, _, err := session.Create(dry, cfg, io.Discard, a.stderr, session.DryRun(a.stdout), session.WithHistory(hist))
	if err == nil && attach {
		return session.RunAttachHook(cfg, a.stderr, session.DryRun(a.stdout))
	}
	return err
}

// dryRunHeader announces a dry run on stdout, ahead of the transcript.
func (a *app) dryRunHeader(lines ...string) {
	for _, l := range lines {
		_, _ = fmt.Fprintln(a.stdout, "# "+l)
	}
}

// teardownDryRun announces a teardown dry run and returns the option that makes
// session.Kill describe itself instead of acting. kill, kill-by-name, and
// restart all want exactly this pair, and each had its own copy of the header
// text — three chances for the wording to drift.
func (a *app) teardownDryRun() []session.Option {
	a.dryRunHeader(
		"dry run: no tmux commands are executed and no",
		"lifecycle hooks are run.")
	return []session.Option{session.DryRun(a.stdout)}
}

// restart tears the session down (running on_project_exit) and builds it
// again from the current config. Editing a config and wanting the session to
// match it is the single most common thing to do next, and `wyrm kill && wyrm`
// is an awkward way to spell it.
func (a *app) restart(args []string) error {
	fs := a.newFlagSet("restart")
	configPath := fs.String("config", "", "path to config file (default: .wyrm.toml, then .tmuxconfig)")
	dryRun := fs.Bool("n", false, "print the tmux commands and hooks that would run, without touching tmux")
	detach := fs.Bool("d", false, "build the session without attaching")
	allFlag := fs.Bool("all", false, "restart all active tmux sessions")
	allShort := fs.Bool("a", false, "alias for -all")
	yesFlag := fs.Bool("y", false, "skip confirmation prompt when restarting all sessions")
	yesLong := fs.Bool("yes", false, "alias for -y")
	var vars varMapFlag
	fs.Var(&vars, "var", "set template variable (KEY=VALUE, can be repeated)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	all := *allFlag || *allShort
	yes := *yesFlag || *yesLong

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	if all {
		if *configPath != "" {
			return usageErrf("-config and -all are mutually exclusive")
		}
		if fs.NArg() > 0 {
			return usageErrf("session name and -all are mutually exclusive")
		}
		return a.restartAll(settings, *dryRun, yes, vars)
	}

	if err := requireNoArgs(fs); err != nil {
		return err
	}

	cfg, _, err := a.resolveConfig(settings, *configPath)
	if err != nil {
		return err
	}
	if len(vars) > 0 {
		cfg.Interpolate(vars)
	}

	hist, err := state.Load()
	if err != nil {
		return err
	}

	if *dryRun {
		// The teardown half consults the real server (see session.Kill's doc),
		// so a not-running session is reported and only the build is described.
		if _, kerr := session.Kill(a.runner, cfg, a.stderr, a.teardownDryRun()...); kerr != nil {
			if !errors.Is(kerr, session.ErrSessionNotRunning) {
				return kerr
			}
			_, _ = fmt.Fprintf(a.stderr, "wyrm: nothing to stop (%v)\n", kerr)
		}
		dry := tmux.NewDryRun(a.stdout)
		_, _, _, err := session.Create(dry, cfg, io.Discard, a.stderr, session.DryRun(a.stdout), session.WithHistory(hist))
		if err == nil && !*detach {
			return session.RunAttachHook(cfg, a.stderr, session.DryRun(a.stdout))
		}
		return err
	}

	if proceed, kerr := a.killForRestart(cfg); !proceed {
		return fmt.Errorf("stopping the existing session: %w", kerr)
	}

	name, sessionID, created, err := session.Create(a.runner, cfg, a.stdout, a.stderr, session.WithHistory(hist))
	if err != nil {
		return err
	}
	a.reportCreated(name, created)
	if *detach {
		_, _ = fmt.Fprintf(a.stdout, "run `wyrm %s` to attach\n", name)
		return nil
	}
	return a.attachSession(cfg, sessionID)
}

func (a *app) restartAll(settings *config.Settings, dryRun, yes bool, vars map[string]string) error {
	active, err := sessions.List(a.runner)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		_, _ = fmt.Fprintln(a.stdout, "no active sessions to restart")
		return nil
	}

	if !dryRun && !yes {
		if !a.promptConfirm(fmt.Sprintf("Restart all %d active tmux session(s)? (y/N): ", len(active))) {
			_, _ = fmt.Fprintln(a.stdout, "aborted")
			return nil
		}
	}

	hist, err := state.Load()
	if err != nil {
		return err
	}

	var opts []session.Option
	if dryRun {
		opts = a.teardownDryRun()
	}

	// One discovery pass for every session below. FindProject re-runs the
	// whole scan — wildcard tree walks included — on each call.
	index := config.NewProjectIndex(settings)
	var failures []error

	for _, s := range active {
		project, found := index.FindSession(s.Name)
		if !found {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: skipping session %q: no project config found\n", s.Name)
			continue
		}
		cfg, err := project.LoadConfig()
		if err != nil {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: skipping session %q: %v\n", s.Name, err)
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		if len(vars) > 0 {
			cfg.Interpolate(vars)
		}

		if dryRun {
			_, _ = fmt.Fprintf(a.stdout, "# Restarting session %s (%s)\n", s.Name, project.Path)
			if _, kerr := session.Kill(a.runner, cfg, a.stderr, opts...); kerr != nil {
				if !errors.Is(kerr, session.ErrSessionNotRunning) {
					failures = append(failures, fmt.Errorf("%s: %w", s.Name, kerr))
					continue
				}
				_, _ = fmt.Fprintf(a.stderr, "wyrm: nothing to stop (%v)\n", kerr)
			}
			dry := tmux.NewDryRun(a.stdout)
			_, _, _, err := session.Create(dry, cfg, io.Discard, a.stderr, session.DryRun(a.stdout), session.WithHistory(hist))
			if err != nil {
				_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: dry-run create failed for %s: %v\n", s.Name, err)
				failures = append(failures, fmt.Errorf("%s: %w", s.Name, err))
			}
			continue
		}

		if proceed, kerr := a.killForRestart(cfg); !proceed {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: skipping session %q: could not stop it: %v\n", s.Name, kerr)
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, kerr))
			continue
		}

		name, _, created, err := session.Create(a.runner, cfg, a.stdout, a.stderr, session.WithHistory(hist))
		if err != nil {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: failed to restart session %s: %v\n", s.Name, err)
			failures = append(failures, fmt.Errorf("%s: %w", s.Name, err))
			continue
		}
		a.reportCreated(name, created)
	}
	return errors.Join(failures...)
}

// kill runs the on_project_exit hook and destroys the session. With a
// positional name it targets that session instead of the current folder's,
// mirroring `wyrm <name>` — killing by name was previously only possible from
// the picker or the TUI.
func (a *app) kill(args []string) error {
	fs := a.newFlagSet("kill")
	configPath := fs.String("config", "", "path to config file (default: .wyrm.toml, then .tmuxconfig)")
	dryRun := fs.Bool("n", false, "print the hook and kill that would run, without touching tmux")
	allFlag := fs.Bool("all", false, "kill all active tmux sessions")
	allShort := fs.Bool("a", false, "alias for -all")
	yesFlag := fs.Bool("y", false, "skip confirmation prompt when killing all sessions")
	yesLong := fs.Bool("yes", false, "alias for -y")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	all := *allFlag || *allShort
	yes := *yesFlag || *yesLong

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	if all {
		if *configPath != "" {
			return usageErrf("-config and -all are mutually exclusive")
		}
		if fs.NArg() > 0 {
			return usageErrf("session name and -all are mutually exclusive")
		}
		return a.killAll(settings, *dryRun, yes)
	}

	if fs.NArg() > 1 {
		return usageErrf("unexpected argument %q (kill takes at most one session name)", fs.Arg(1))
	}

	// A named target resolves through the project list first, so its
	// on_project_exit hook still runs when wyrm knows the config. Falling back
	// to a plain tmux kill covers sessions wyrm didn't create.
	if target := fs.Arg(0); target != "" {
		if *configPath != "" {
			return usageErrf("-config and a session name are mutually exclusive")
		}
		return a.killByName(settings, target, *dryRun)
	}

	cfg, _, err := a.resolveConfig(settings, *configPath)
	if err != nil {
		return err
	}

	var opts []session.Option
	if *dryRun {
		opts = a.teardownDryRun()
	}
	name, err := session.Kill(a.runner, cfg, a.stderr, opts...)
	if err != nil {
		return err
	}
	if !*dryRun {
		_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", name)
	}
	return nil
}

func (a *app) killAll(settings *config.Settings, dryRun, yes bool) error {
	active, err := sessions.List(a.runner)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		_, _ = fmt.Fprintln(a.stdout, "no active sessions to kill")
		return nil
	}

	if !dryRun && !yes {
		if !a.promptConfirm(fmt.Sprintf("Kill all %d active tmux session(s)? (y/N): ", len(active))) {
			_, _ = fmt.Fprintln(a.stdout, "aborted")
			return nil
		}
	}

	var opts []session.Option
	if dryRun {
		opts = a.teardownDryRun()
	}

	index := config.NewProjectIndex(settings)
	var failures []error

	for _, s := range active {
		if project, found := index.FindSession(s.Name); found {
			if cfg, err := project.LoadConfig(); err == nil {
				name, kerr := session.Kill(a.runner, cfg, a.stderr, opts...)
				if kerr != nil {
					_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: failed to kill session %s: %v\n", s.Name, kerr)
					failures = append(failures, fmt.Errorf("%s: %w", s.Name, kerr))
				} else if !dryRun {
					_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", name)
				}
				continue
			}
		}

		if dryRun {
			_, _ = fmt.Fprintf(a.stdout, "tmux kill-session -t %s\n", s.ID)
		} else {
			if out, err := a.runner.Run("kill-session", "-t", s.ID); err != nil {
				_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: failed to kill session %s: %v\n", s.Name, tmux.CmdErr(err, out))
				failures = append(failures, fmt.Errorf("%s: %w", s.Name, tmux.CmdErr(err, out)))
			} else {
				_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", s.Name)
			}
		}
	}
	return errors.Join(failures...)
}

func (a *app) killByName(settings *config.Settings, target string, dryRun bool) error {
	var opts []session.Option
	if dryRun {
		opts = a.teardownDryRun()
	}

	// An exact running session takes precedence over a configured project or
	// alias — attachByName resolves in the same order. Without this, a
	// project's alias colliding with an unrelated live session's exact name
	// made `wyrm kill <name>` destroy the wrong session.
	id, ok, err := tmux.FindSessionID(a.runner, target)
	if err != nil {
		return err
	}
	if ok {
		if project, found := config.FindProject(settings, target); found {
			if cfg, cerr := project.LoadConfig(); cerr == nil {
				if name, _, rerr := cfg.Session.Resolve(cfg.Dir()); rerr == nil && name == target {
					name, kerr := session.Kill(a.runner, cfg, a.stderr, opts...)
					if kerr != nil {
						return kerr
					}
					if !dryRun {
						_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", name)
					}
					return nil
				}
			}
		}
		// No config resolves to this exact session — either there is none, or
		// the match above was only through another project's alias — so
		// there's no hook to run, just the kill itself.
		if dryRun {
			_, _ = fmt.Fprintf(a.stdout, "tmux kill-session -t %s\n", id)
			return nil
		}
		if err := sessions.Kill(a.runner, id); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", target)
		return nil
	}

	// Nothing running by that exact name: fall back to a configured project
	// or alias, mirroring attachByName/startProject. There is no live session
	// to misidentify here, so trusting the config match is safe.
	if project, found := config.FindProject(settings, target); found {
		if cfg, err := project.LoadConfig(); err == nil {
			name, kerr := session.Kill(a.runner, cfg, a.stderr, opts...)
			if kerr != nil {
				return kerr
			}
			if !dryRun {
				_, _ = fmt.Fprintf(a.stdout, "killed session %s\n", name)
			}
			return nil
		}
	}

	return fmt.Errorf("no running session named %q", target)
}

// attachByName attaches or switches directly to the exact-named running
// session, without the interactive picker. This is what shell completion (see
// completions/) completes a bare positional argument to.
//
// Because run()'s default case can't distinguish a fat-fingered subcommand
// from a genuine session name (see knownSubcommands), a not-found error here
// also hints at the nearest known verb when name looks like a typo of one —
// so `wyrm klil` says more than just "no running session named klil".
func (a *app) attachByName(name string, extraArgs []string) error {
	var vars varMapFlag
	if len(extraArgs) > 0 {
		fs := a.newFlagSet(name)
		fs.Var(&vars, "var", "set template variable (KEY=VALUE, can be repeated)")
		if err := parseFlags(fs, extraArgs); err != nil {
			return err
		}
		if err := requireNoArgs(fs); err != nil {
			return err
		}
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	id, ok, err := tmux.FindSessionID(a.runner, name)
	if err != nil {
		return err
	}
	if ok {
		actualName, nerr := tmux.SessionName(a.runner, id)
		if nerr != nil {
			return nerr
		}
		if project, found := config.NewProjectIndex(settings).FindSession(actualName); found {
			if cfg, err := project.LoadConfig(); err == nil {
				if len(vars) > 0 {
					cfg.Interpolate(vars)
				}
				return a.attachSession(cfg, id)
			}
		}
		return a.attachOrSwitch(id)
	}

	// Nothing running by that name — but wyrm may know a *config* by it. This
	// is what makes shared storage worth using: without it, centralizing every
	// project's config buys nothing, because the only way to start one is still
	// to cd into its folder first.
	if project, found := config.FindProject(settings, name); found {
		return a.startProject(project, vars)
	}

	_, _ = fmt.Fprintf(a.stderr, "wyrm: no running session or known project named %q\n", name)
	if guess, ok := nearestSubcommand(name); ok {
		_, _ = fmt.Fprintf(a.stderr, "wyrm: did you mean the subcommand %q?\n", guess)
	}
	return silent(1)
}

// startProject builds (or reattaches) the session for a discovered config and
// hands the terminal over.
func (a *app) startProject(project config.Project, vars map[string]string) error {
	cfg, err := project.LoadConfig()
	if err != nil {
		return err
	}
	if len(vars) > 0 {
		cfg.Interpolate(vars)
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: using config %s\n", project.Path)
	a.printWarnings(cfg)
	if msg, bad := config.CheckSharedRoot(project, cfg); bad {
		_, _ = fmt.Fprintln(a.stderr, "wyrm: warning: "+msg)
	}

	hist, err := state.Load()
	if err != nil {
		return err
	}

	name, sessionID, created, err := session.Create(a.runner, cfg, a.stdout, a.stderr, session.WithHistory(hist))
	if err != nil {
		return err
	}
	a.reportCreated(name, created)
	return a.attachSession(cfg, sessionID)
}
