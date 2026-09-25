package main

// clone clones a git repository and starts a session for it in one step —
// wyrm's second-ever dependency on a binary other than tmux (after
// selfupdate's use of the network), scoped to this one explicit subcommand
// rather than any default behavior.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
)

// clone runs `git clone`, then behaves like a bare `wyrm up` run from inside
// the freshly cloned directory: local config, shared config (named for the
// directory), user default, or built-in default — whichever discovery would
// already find. If a [[wildcard]] pattern covers the destination, that
// template is used instead, the same as it would be for any other directory
// under the pattern.
func (a *app) clone(args []string) error {
	fs := a.newFlagSet("clone")
	noStart := fs.Bool("no-start", false, "clone the repository without starting a session")
	fs.BoolVar(noStart, "n", false, "alias for -no-start")
	yes := fs.Bool("y", false, "start the session without confirming the config's shell commands")
	fs.BoolVar(yes, "yes", false, "alias for -y")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return usageErrf("wyrm clone needs a repository and an optional destination directory: wyrm clone [-no-start] <repo> [dest]")
	}
	repo, dest := fs.Arg(0), fs.Arg(1)

	if strings.HasPrefix(repo, "-") {
		return usageErrf("invalid repository %q", repo)
	}
	if dest != "" && strings.HasPrefix(dest, "-") {
		return usageErrf("invalid destination directory %q", dest)
	}

	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("wyrm clone requires git on PATH")
	}

	gitArgs := []string{"clone", "--", repo}
	if dest != "" {
		gitArgs = append(gitArgs, dest)
	}
	cmd := exec.Command("git", gitArgs...)
	cmd.Stdout, cmd.Stderr = a.stdout, a.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}

	if dest == "" {
		dest = deriveCloneDir(repo)
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	if *noStart {
		_, _ = fmt.Fprintf(a.stdout, "cloned %s to %s\n", repo, absDest)
		return nil
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	// A wildcard pattern covering the destination takes priority over
	// whatever discovery would find inside it — matching how any other
	// directory under that pattern behaves, clone included or not.
	project, found := config.FindProject(settings, filepath.Base(absDest))
	useWildcard := found && project.Wildcard && project.Root == absDest

	if !useWildcard {
		if err := os.Chdir(absDest); err != nil {
			return fmt.Errorf("entering %s: %w", absDest, err)
		}
	}

	if !*yes {
		ok, err := a.confirmClonedConfig(settings, project, useWildcard)
		if err != nil {
			return err
		}
		if !ok {
			_, _ = fmt.Fprintf(a.stdout,
				"cloned %s to %s; not started\nreview the config, then run `wyrm` in that directory\n", repo, absDest)
			return nil
		}
	}

	if useWildcard {
		return a.startProject(project, nil)
	}
	return a.up(nil)
}

// confirmClonedConfig shows the shell a freshly cloned repository's config
// would run and asks whether to go ahead.
//
// wyrm configs execute shell by design, and that is the documented trust
// model — but `wyrm clone` is the one verb whose input is a repository the
// user has not read yet. It used to print "run with -no-start to clone
// without executing hooks" and then execute them anyway, naming the flag you
// needed *before* you ran the command. Now the choice is offered while it can
// still be made.
//
// promptConfirm returns false when stdin cannot be read, so a non-interactive
// run declines rather than proceeding unattended.
func (a *app) confirmClonedConfig(settings *config.Settings, project config.Project, useWildcard bool) (bool, error) {
	var cfg *config.Config
	var err error
	if useWildcard {
		cfg, err = project.LoadConfig()
	} else {
		// The config `up` will actually use, which is not necessarily one in
		// the clone: discovery also reaches the shared directory and parent
		// directories.
		cfg, _, err = config.ResolveEffective(settings, "")
	}
	if err != nil {
		// Nothing loadable to inspect; `up` will report the same failure
		// properly, with its own message.
		return true, nil //nolint:nilerr
	}
	commands := configCommands(cfg)
	if len(commands) == 0 {
		return true, nil
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: this config runs shell commands:\n")
	for _, c := range commands {
		_, _ = fmt.Fprintf(a.stderr, "  %s\n", c)
	}
	return a.promptConfirm("Start the session and run them? (y/N): "), nil
}

// configCommands lists every piece of shell a config would execute, labelled
// by where it comes from. Hooks run as real subprocesses; pane commands are
// typed into a shell. Both execute, so both are shown — the old check looked
// only at on_project_start and on_project_first_start.
func configCommands(cfg *config.Config) []string {
	var out []string
	add := func(label, cmd string) {
		if cmd != "" {
			out = append(out, label+" = "+cmd)
		}
	}
	add("on_project_start", cfg.Session.OnProjectStart)
	add("on_project_first_start", cfg.Session.OnProjectFirstStart)
	add("on_project_restart", cfg.Session.OnProjectRestart)
	add("on_project_attach", cfg.Session.OnProjectAttach)
	add("on_project_exit", cfg.Session.OnProjectExit)
	add("on_project_detach", cfg.Session.OnProjectDetach)
	if cfg.Session.EnablePaneTitles != nil && *cfg.Session.EnablePaneTitles && strings.Contains(cfg.Session.PaneTitleFormat, "#(") {
		add("pane_title_format (tmux command substitution)", cfg.Session.PaneTitleFormat)
	}
	for _, w := range cfg.Windows {
		where := "window " + w.Name
		add(where+" pre_window", w.PreWindow)
		add(where+" post_window", w.PostWindow)
		out = append(out, splitCommands(where, w.Splits)...)
		for _, p := range w.Panes {
			add(where+" pane", p.Command)
		}
	}
	return out
}

func splitCommands(where string, splits []config.Split) []string {
	var out []string
	for _, s := range splits {
		if s.Command != "" {
			out = append(out, where+" command = "+s.Command)
		}
		if s.Run != "" {
			out = append(out, where+" run = "+s.Run)
		}
		out = append(out, splitCommands(where, s.Children)...)
	}
	return out
}

// deriveCloneDir mirrors git's own destination-directory derivation when no
// explicit dest is given: the repository's name with a trailing ".git"
// (and any trailing slash) stripped, taking the last path segment so an
// scp-style "host:user/repo.git" address resolves the same way a URL does.
func deriveCloneDir(repo string) string {
	trimmed := strings.TrimSuffix(strings.TrimRight(repo, "/"), ".git")
	if !strings.Contains(trimmed, "/") {
		if _, path, ok := strings.Cut(trimmed, ":"); ok && path != "" {
			trimmed = path
		}
	}
	return filepath.Base(trimmed)
}
