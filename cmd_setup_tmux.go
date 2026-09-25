package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
)

// setupTmux generates or appends recommended tmux keybindings and popup configurations.
func (a *app) setupTmux(args []string) error {
	fs := a.newFlagSet("setup-tmux")
	appendConf := fs.Bool("a", false, "append configuration directly to tmux.conf")
	fs.BoolVar(appendConf, "append", false, "append configuration directly to tmux.conf")
	fs.BoolVar(appendConf, "write", false, "append configuration directly to tmux.conf")
	fs.BoolVar(appendConf, "w", false, "append configuration directly to tmux.conf")
	keyPick := fs.String("key-pick", "C-j", "prefix key combination to open wyrm pick popup")
	keyTUI := fs.String("key-tui", "C-w", "prefix key combination to open wyrm tui popup")
	status := fs.Bool("status", true, "include wyrm status bar integration")

	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	if err := validateKeySpec("key-pick", *keyPick); err != nil {
		return err
	}
	if err := validateKeySpec("key-tui", *keyTUI); err != nil {
		return err
	}

	snippet := generateTmuxSnippet(*keyPick, *keyTUI, *status)

	if !*appendConf {
		_, _ = fmt.Fprint(a.stdout, snippet)
		return nil
	}

	targetPath := findTmuxConfPath()
	return appendTmuxConf(targetPath, snippet, a.stdout)
}

func generateTmuxSnippet(keyPick, keyTUI string, status bool) string {
	var b strings.Builder
	b.WriteString("# --- wyrm tmux integration ---\n")
	fmt.Fprintf(&b, "# Float wyrm session picker with Prefix + %s\n", keyPick)
	fmt.Fprintf(&b, "bind-key %s display-popup -E -w 80%% -h 80%% \"wyrm pick\"\n\n", keyPick)
	fmt.Fprintf(&b, "# Float full wyrm TUI session manager with Prefix + %s\n", keyTUI)
	fmt.Fprintf(&b, "bind-key %s display-popup -E -w 90%% -h 85%% \"wyrm tui\"\n", keyTUI)
	if status {
		b.WriteString("\n# Optional status bar agent indicator\n")
		b.WriteString("set -g status-right '#(wyrm status --format tmux) | %H:%M '\n")
	}
	b.WriteString("# --- end wyrm tmux integration ---\n")
	return b.String()
}

func findTmuxConfPath() string {
	if conf := os.Getenv("TMUX_CONF"); conf != "" {
		return conf
	}
	// $XDG_CONFIG_HOME, not a hardcoded ~/.config — this used to be the one
	// path in the codebase that ignored it, so a user with XDG_CONFIG_HOME set
	// had the snippet appended to a file tmux never reads.
	if dir, err := config.UserConfigDir(); err == nil {
		xdgPath := filepath.Join(dir, "tmux", "tmux.conf")
		if _, err := os.Stat(xdgPath); err == nil {
			return xdgPath
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tmux.conf"
	}
	return filepath.Join(home, ".tmux.conf")
}

// tmuxKeySpec matches the key specifications tmux's bind-key accepts: any
// number of C-/M-/S- modifiers followed by a single character or a named key
// (F1, Up, PageDown, BSpace...).
var tmuxKeySpec = regexp.MustCompile(`^([CMS]-)*([[:graph:]]|[A-Za-z][A-Za-z0-9]*)$`)

// validateKeySpec rejects a key wyrm would otherwise write straight into the
// user's tmux.conf. bind-key fails at config-load time, so a bad spec here does
// not break just wyrm's two bindings — it aborts the rest of the file, and the
// user finds out at their next tmux start with no clue which line did it.
func validateKeySpec(flag, key string) error {
	if key == "" {
		return usageErrf("-%s cannot be empty", flag)
	}
	if strings.ContainsAny(key, " \t\"'#;") {
		return usageErrf("-%s %q contains a character tmux.conf cannot quote safely", flag, key)
	}
	if !tmuxKeySpec.MatchString(key) {
		return usageErrf("-%s %q is not a tmux key specification (want e.g. C-j, M-x, F5, Up)", flag, key)
	}
	return nil
}

func appendTmuxConf(path, snippet string, stdout io.Writer) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	if bytes.Contains(existing, []byte("# --- wyrm tmux integration ---")) {
		_, _ = fmt.Fprintf(stdout, "wyrm tmux integration is already present in %s\n", path)
		return nil
	}

	// Back up first. This is the user's own tmux.conf, not a wyrm file, and
	// appending to it in place left no way back if the result did not load.
	if len(existing) > 0 {
		backup := path + ".wyrm-backup"
		if err := os.WriteFile(backup, existing, 0o600); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
		_, _ = fmt.Fprintf(stdout, "backed up %s to %s\n", path, backup)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s for writing: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := f.WriteString("\n" + snippet); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(stdout, "appended wyrm integration to %s\n", path)
	return nil
}
