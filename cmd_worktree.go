package main

// `wyrm worktree BRANCH`: create (or reuse) a git worktree for a branch next
// to the repository and start a session in it from the repository's config.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/state"
)

func (a *app) worktree(args []string) error {
	fs := a.newFlagSet("worktree")
	base := fs.String("base", "", "start a new branch from this ref (default: the current HEAD)")
	dest := fs.String("path", "", "where to put the worktree (default: <repo>.worktrees/<branch> next to the repository)")
	detach := fs.Bool("d", false, "start the session without attaching")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usageErrf("wyrm worktree needs exactly one branch name")
	}
	branch := fs.Arg(0)
	if strings.HasPrefix(branch, "-") {
		return usageErrf("invalid branch name %q", branch)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("wyrm worktree requires git on PATH")
	}
	top, err := gitOutput("", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not inside a git repository: %w", err)
	}

	// Default: <repo>.worktrees/<branch> beside the repository. Session names
	// already combine the main repository's name with the worktree directory's
	// (see config's worktree naming), so a directory that repeated the repo
	// name would name the session "app-app-login".
	path := *dest
	if path == "" {
		path = filepath.Join(filepath.Dir(top), filepath.Base(top)+".worktrees", worktreeDirName(branch))
	}
	if path, err = filepath.Abs(path); err != nil {
		return err
	}

	if _, statErr := os.Stat(path); statErr == nil {
		_, _ = fmt.Fprintf(a.stderr, "wyrm: %s already exists; using it\n", path)
	} else {
		gitArgs := []string{"worktree", "add"}
		if _, err := gitOutput(top, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			gitArgs = append(gitArgs, "--", path, branch)
		} else {
			gitArgs = append(gitArgs, "-b", branch, "--", path)
			if *base != "" {
				gitArgs = append(gitArgs, *base)
			}
		}
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = top
		cmd.Stdout, cmd.Stderr = a.stderr, a.stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git worktree add: %w", err)
		}
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	cfg, source, err := worktreeConfig(top, path)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.stderr, "wyrm: using config %s\n", source)
	a.printWarnings(cfg)
	if err := a.ensureTrusted(settings, cfg); err != nil {
		return err
	}
	hist, err := state.Load()
	if err != nil {
		return err
	}
	name, id, created, err := session.Create(a.runner, cfg, a.stdout, a.stderr, session.WithHistory(hist))
	if err != nil {
		return err
	}
	a.reportCreated(name, created)
	if *detach {
		_, _ = fmt.Fprintf(a.stdout, "run `wyrm %s` to attach\n", name)
		return nil
	}
	return a.attachSession(settings, cfg, id)
}

// worktreeConfig picks the config for a worktree at path: the worktree's own
// committed config if it has one, else the main checkout's, rerooted at the
// worktree. Either way session.name is cleared, so the session is named
// "<repo>-<worktree>" (see config's worktree naming) — an explicit name would
// give every worktree of the repository the same session.
func worktreeConfig(top, path string) (*config.Config, string, error) {
	if found, err := config.DiscoverIn(path, false); err == nil {
		if !filepath.IsAbs(found) {
			found = filepath.Join(path, found)
		}
		cfg, err := config.Load(found)
		if err != nil {
			return nil, "", err
		}
		cfg.Session.Name = ""
		return cfg, found, nil
	}
	found, err := config.DiscoverIn(top, false)
	if err != nil {
		cfg, err := config.LoadDefault()
		if err != nil {
			return nil, "", err
		}
		cfg.Session.Name, cfg.Session.Root = "", path
		return cfg, "built-in default", nil
	}
	if !filepath.IsAbs(found) {
		found = filepath.Join(top, found)
	}
	cfg, err := config.Load(found)
	if err != nil {
		return nil, "", err
	}
	cfg.Session.Name, cfg.Session.Root = "", path
	return cfg, found, nil
}

// worktreeDirName turns a branch into a directory name: "feature/login"
// becomes "feature-login".
func worktreeDirName(branch string) string {
	return strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(branch)
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
