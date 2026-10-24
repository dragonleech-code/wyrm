package main

// Remote builds: `wyrm up -host H` and `wyrm kill -host H` drive tmux on
// another machine over ssh. The config is read here and built there; see
// tmux.SSH and config.Config.ForRemote.

import (
	"fmt"
	"io"
	"time"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/state"
	"github.com/jskoll/wyrm/internal/tmux"
)

// newSSHRunner is how remote verbs reach a host, swapped by tests for a
// recording double.
var newSSHRunner = func(host string, settings *config.Settings) remoteRunner {
	return tmux.SSH{Host: host, SocketName: settings.TmuxSocket(), Command: settings.TmuxCommand()}
}

// remoteRunner is what a remote build needs from tmux.SSH.
type remoteRunner interface {
	tmux.Runner
	Attach(target string) error
	Home() (string, error)
	RunShell(command, dir string, env []string, timeout time.Duration, w io.Writer) error
}

// hostHistory keys lifecycle history by host as well as root, so a remote
// project at /srv/api and a local one at the same path keep separate
// first-start records.
type hostHistory struct {
	host  string
	inner *state.Store
}

func (h hostHistory) key(dir string) string { return "ssh://" + h.host + dir }

func (h hostHistory) Started(dir string) bool { return h.inner.Started(h.key(dir)) }

func (h hostHistory) MarkStarted(dir string) error { return h.inner.MarkStarted(h.key(dir)) }

// remoteOptions are the session options every remote build or kill uses:
// hooks run on the host, and wait_for gates call the host's own wyrm.
func remoteOptions(r remoteRunner) []session.Option {
	return []session.Option{
		session.WithHookRunner(func(spec session.HookSpec) error {
			return r.RunShell(spec.Command, spec.Dir, spec.Env, spec.Timeout, spec.Stderr)
		}),
		session.WithWaitCommand("wyrm"),
	}
}

// prepareRemote rebases cfg onto the host's home directory.
func prepareRemote(r remoteRunner, cfg *config.Config) error {
	home, err := r.Home()
	if err != nil {
		return err
	}
	return cfg.ForRemote(home)
}

func (a *app) upRemote(settings *config.Settings, cfg *config.Config, host string, dryRun, detach bool) error {
	r := newSSHRunner(host, settings)
	if err := prepareRemote(r, cfg); err != nil {
		return err
	}
	hist, err := state.Load()
	if err != nil {
		return err
	}
	if dryRun {
		a.dryRunHeader("dry run: no tmux commands are executed on " + host + ", no")
		a.dryRunHeader("lifecycle hooks are run.")
		_, _, _, err := session.Create(tmux.NewDryRun(a.stdout), cfg, io.Discard, a.stderr,
			append(remoteOptions(r), session.DryRun(a.stdout), session.WithHistory(hostHistory{host, hist}))...)
		return err
	}
	if err := a.ensureTrusted(settings, cfg); err != nil {
		return err
	}
	opts := append(remoteOptions(r), session.WithHistory(hostHistory{host, hist}))
	name, id, created, err := session.Create(r, cfg, a.stdout, a.stderr, opts...)
	if err != nil {
		return err
	}
	a.reportCreated(name, created)
	if detach {
		_, _ = fmt.Fprintf(a.stdout, "run `ssh -t %s tmux attach -t %s` to attach\n", host, tmux.Quote(name))
		return nil
	}
	_ = session.RunAttachHook(cfg, a.stderr, remoteOptions(r)...)
	return r.Attach(id)
}

func (a *app) killRemote(settings *config.Settings, cfg *config.Config, host string, dryRun bool) error {
	r := newSSHRunner(host, settings)
	if err := prepareRemote(r, cfg); err != nil {
		return err
	}
	opts := remoteOptions(r)
	if dryRun {
		opts = append(opts, a.teardownDryRun()...)
	} else {
		var err error
		if opts, err = a.teardownOptions(settings, cfg, opts); err != nil {
			return err
		}
	}
	name, err := session.Kill(r, cfg, a.stderr, opts...)
	if err != nil {
		return err
	}
	if !dryRun {
		_, _ = fmt.Fprintf(a.stdout, "killed session %s on %s\n", name, host)
	}
	return nil
}
