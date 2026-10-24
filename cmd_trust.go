package main

// Trust: the prompt that guards running a project config wyrm didn't get from
// the user's own configuration, and the allow/deny verbs that manage it. See
// internal/trust for the model.

import (
	"errors"
	"fmt"
	"time"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/trust"
)

// errUntrusted marks a config the user has not agreed to run. Callers that
// destroy a session (kill) still proceed without its hooks; callers that build
// one stop.
var errUntrusted = errors.New("config not trusted")

// ensureTrusted returns nil when cfg may run: it needs no trust decision, or
// the user trusted it as it is now. Otherwise it lists the shell cfg would run
// and asks, recording a yes. A no, an unreadable stdin, or an earlier deny is
// an error wrapping errUntrusted.
func (a *app) ensureTrusted(settings *config.Settings, cfg *config.Config) error {
	if !trust.Required(settings, cfg) {
		return nil
	}
	store, err := trust.Load()
	if err != nil {
		return err
	}
	path, fingerprint := cfg.Path(), cfg.Fingerprint()
	switch store.Check(path, fingerprint) {
	case trust.Trusted:
		return nil
	case trust.Denied:
		return fmt.Errorf("%w: you denied %s; once you've reviewed it, run `wyrm allow %s`", errUntrusted, path, path)
	case trust.Changed:
		_, _ = fmt.Fprintf(a.stderr, "wyrm: %s has changed since you trusted it.\n", path)
	default:
		_, _ = fmt.Fprintf(a.stderr, "wyrm: %s is not trusted yet.\n", path)
	}
	if bases := cfg.Sources()[1:]; len(bases) > 0 {
		_, _ = fmt.Fprintf(a.stderr, "wyrm: it extends %v\n", bases)
	}
	_, _ = fmt.Fprintln(a.stderr, "wyrm: this config runs shell commands:")
	for _, c := range cfg.Commands() {
		_, _ = fmt.Fprintf(a.stderr, "  %s\n", c)
	}
	if !a.promptConfirm("Trust this config and run it? (y/N): ") {
		return fmt.Errorf("%w: not running %s (review it, then `wyrm allow %s`)", errUntrusted, path, path)
	}
	return store.Allow(path, fingerprint)
}

// teardownOptions returns the session options for destroying cfg's session:
// with its hooks when cfg is trusted, without them (and a warning) when it is
// not. Killing a session should not depend on trusting its config — only
// running the config's shell should.
func (a *app) teardownOptions(settings *config.Settings, cfg *config.Config, opts []session.Option) ([]session.Option, error) {
	err := a.ensureTrusted(settings, cfg)
	if err == nil {
		return opts, nil
	}
	if !errors.Is(err, errUntrusted) {
		return nil, err
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: %v; stopping the session without its hooks\n", err)
	return append(opts, session.SkipHooks()), nil
}

// allow trusts a config, or with -list prints every decision on record.
func (a *app) allow(args []string) error {
	fs := a.newFlagSet("allow")
	list := fs.Bool("list", false, "list every trusted and denied config")
	revoke := fs.Bool("revoke", false, "forget the decision about the config, so wyrm asks again")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usageErrf("allow takes at most one config path")
	}
	store, err := trust.Load()
	if err != nil {
		return err
	}
	if *list {
		for _, e := range store.Entries() {
			status := "trusted"
			if e.Denied {
				status = "denied "
			}
			_, _ = fmt.Fprintf(a.stdout, "%s  %s  %s\n", status, e.At.Local().Format(time.DateOnly), e.Path)
		}
		return nil
	}
	cfg, err := a.trustTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	if *revoke {
		if err := store.Forget(cfg.Path()); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(a.stdout, "forgot %s\n", cfg.Path())
		return nil
	}
	if err := store.Allow(cfg.Path(), cfg.Fingerprint()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.stdout, "trusted %s\n", cfg.Path())
	return nil
}

// deny refuses a config: wyrm will not run it, nor ask about it again.
func (a *app) deny(args []string) error {
	fs := a.newFlagSet("deny")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usageErrf("deny takes at most one config path")
	}
	cfg, err := a.trustTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	store, err := trust.Load()
	if err != nil {
		return err
	}
	if err := store.Deny(cfg.Path()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.stdout, "denied %s\n", cfg.Path())
	return nil
}

// trustTarget loads the config allow/deny act on: the path given, or the one
// a bare `wyrm` here would use.
func (a *app) trustTarget(path string) (*config.Config, error) {
	settings, err := config.LoadSettings()
	if err != nil {
		return nil, err
	}
	cfg, source, err := config.ResolveEffective(settings, path)
	if err != nil {
		return nil, err
	}
	if cfg.Path() == "" {
		return nil, fmt.Errorf("%s has no file to trust", source)
	}
	return cfg, nil
}
