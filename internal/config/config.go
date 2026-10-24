// Package config loads and validates wyrm session configuration files.
package config

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jskoll/wyrm/internal/wait"
	"github.com/pelletier/go-toml/v2"
)

// DefaultFileName is the config file wyrm looks for first.
const DefaultFileName = ".wyrm.toml"

// LegacyFileName is the original tmux-session config name, still supported.
const LegacyFileName = ".tmuxconfig"

// defaultConfigData is the built-in config used when neither DefaultFileName
// nor LegacyFileName is found in the current directory.
//
//go:embed default.wyrm.toml
var defaultConfigData []byte

// Config is the root of a wyrm config file.
type Config struct {
	// Extends names a base config this one builds on, resolved against this
	// file's directory. See mergeBase for how the two combine.
	Extends string   `toml:"extends,omitempty"`
	Session Session  `toml:"session"`
	Windows []Window `toml:"windows"`

	// dir is the directory the config was loaded from. Relative roots resolve
	// against it rather than against the process's working directory, so a
	// config means the same thing however wyrm was invoked — see Session.Resolve
	// and Config.Dir. Zero for a Config built in memory (tests, the built-in
	// default), which falls back to the working directory.
	dir string

	// path is the absolute path of the file this config was loaded from, and
	// sources every file that contributed to it: path first, then each config
	// it extends. digest fingerprints their contents as read, so a trust
	// decision covers exactly the bytes that will run — see Fingerprint.
	path    string
	sources []string
	digest  string

	// warnings are non-fatal problems found while validating: things that will
	// build, but not the way the config's author probably meant. Callers print
	// them; they never stop a session being created.
	warnings []string
}

// Dir returns the directory this config was loaded from, or "" for a config
// that didn't come from a file.
func (c *Config) Dir() string { return c.dir }

// Path returns the absolute path of the file this config was loaded from, or
// "" for a config that didn't come from a file.
func (c *Config) Path() string { return c.path }

// Sources returns every file that contributed to this config: its own path,
// then each base it extends, nearest first.
func (c *Config) Sources() []string { return c.sources }

// Fingerprint is a SHA-256 over the path and content of every source file, as
// read when the config was loaded. It changes whenever any file in the extends
// chain does, so trusting a fingerprint trusts the whole chain.
func (c *Config) Fingerprint() string { return c.digest }

// Warnings returns non-fatal problems found while validating the config.
func (c *Config) Warnings() []string { return c.warnings }

// Session describes the tmux session and its lifecycle hooks.
type Session struct {
	Name string `toml:"name,omitempty"`
	Root string `toml:"root,omitempty"`
	// ProjectDir records where a migrated shared config was discovered. Its
	// session root may be a subdirectory, so root alone is not its owner.
	ProjectDir      string `toml:"project_dir,omitempty"`
	OnProjectStart  string `toml:"on_project_start,omitempty"`
	OnProjectExit   string `toml:"on_project_exit,omitempty"`
	OnProjectAttach string `toml:"on_project_attach,omitempty"`
	OnProjectDetach string `toml:"on_project_detach,omitempty"`
	// OnProjectFirstStart and OnProjectRestart run alongside OnProjectStart —
	// which always fires on a fresh build — distinguishing a project's
	// genuine first-ever start from a later one. Which fires is decided by
	// session.HookHistory, threaded in from outside this package: Create has
	// no way to tell the two apart on its own without it.
	OnProjectFirstStart string `toml:"on_project_first_start,omitempty"`
	OnProjectRestart    string `toml:"on_project_restart,omitempty"`
	// HookTimeout bounds every lifecycle hook and post_window command, as a Go
	// duration ("90s", "5m"). Empty means no limit.
	HookTimeout   string            `toml:"hook_timeout,omitempty"`
	StartupWindow string            `toml:"startup_window,omitempty"`
	StartupPane   *int              `toml:"startup_pane,omitempty"` // nil = unset; 0 is a valid pane
	Env           map[string]string `toml:"env,omitempty"`
	// Aliases are additional exact-match names FindProject resolves this
	// project by, alongside its normal session name — a short, fixed name
	// that doesn't shift as other projects come and go. Matched only for
	// exact equality, the same as the session name itself; no fuzzy
	// matching is involved.
	Aliases []string `toml:"aliases,omitempty"`

	// EnablePaneTitles turns on tmux's live pane-border status line
	// (pane-border-status/-format) for the session. Nil means off, matching
	// tmux's own default.
	EnablePaneTitles *bool `toml:"enable_pane_titles,omitempty"`
	// PaneTitlePosition is "top" (default) or "bottom". Validated in
	// (*Config).validate.
	PaneTitlePosition string `toml:"pane_title_position,omitempty"`
	// PaneTitleFormat is a tmux format string; empty uses
	// "#{pane_index}: #{pane_current_command}".
	PaneTitleFormat string `toml:"pane_title_format,omitempty"`
}

// Window is one tmux window, laid out either by a split tree or a flat pane
// list (legacy format).
type Window struct {
	Name   string `toml:"name,omitempty"`
	Layout string `toml:"layout,omitempty"`
	// Root is this window's working directory. Relative paths resolve against
	// the session root, so a monorepo can say root = "api" and get a window
	// rooted there. Empty means the session root.
	//
	// Prefer it to pre_window = "cd api", which types a visible cd into every
	// pane of the window and races that pane's own command.
	Root      string            `toml:"root,omitempty"`
	Env       map[string]string `toml:"env,omitempty"`
	Splits    []Split           `toml:"splits,omitempty"`
	Panes     []Pane            `toml:"panes,omitempty"`
	PreWindow string            `toml:"pre_window,omitempty"`
	// PostWindow is a shell command run (via your $SHELL, or sh, in the
	// window's root) once all of the window's panes exist, splits and pane
	// commands included. Unlike PreWindow, it is a real subprocess — not
	// typed into a pane — giving an out-of-band point for something like
	// "wait for a port to open before continuing", the same shape as the
	// project-level on_project_start/on_project_exit hooks.
	PostWindow string `toml:"post_window,omitempty"`
	// Synchronize turns on tmux's synchronize-panes for this window on creation.
	Synchronize *bool `toml:"synchronize,omitempty"`
	// SynchronizePanes is an alias for Synchronize.
	SynchronizePanes *bool `toml:"synchronize_panes,omitempty"`
	// RemainOnExit keeps panes open after their process exits so crash logs are inspectable.
	RemainOnExit *bool `toml:"remain_on_exit,omitempty"`
}

// Split is a node in a window's split tree.
type Split struct {
	Type string `toml:"type,omitempty"` // "", "h"/"horizontal", "v"/"vertical"
	Size int    `toml:"size,omitempty"` // percentage for the new pane; 0 = tmux default
	// Command is typed into the pane's shell, as if you had typed it.
	Command string `toml:"command,omitempty"`
	// Run makes the command the pane's own process instead of typing it into a
	// shell. There is no shell underneath, so the pane closes when it exits
	// (unless tmux's remain-on-exit is set) — which is exactly what you want for
	// a long-running server and exactly what you don't want for a prompt you
	// mean to keep using.
	//
	// It also sidesteps the two things typing can't do: the text never lands in
	// shell history, and a command starting with "#" is runnable (Command treats
	// those as comments).
	Run string `toml:"run,omitempty"`
	// WaitFor delays this pane's command or run until a dependency is ready:
	// "tcp:HOST:PORT", "file:PATH", or an http(s) URL. The pane itself is
	// created immediately; only the command waits, via `wyrm wait`.
	WaitFor string `toml:"wait_for,omitempty"`
	// WaitTimeout bounds WaitFor, as a Go duration. Empty means
	// wait.DefaultTimeout. The command does not run if the wait times out.
	WaitTimeout string `toml:"wait_timeout,omitempty"`
	// Root overrides the window's directory for this pane and, unless they
	// override it themselves, its children.
	Root     string            `toml:"root,omitempty"`
	Env      map[string]string `toml:"env,omitempty"`
	Children []Split           `toml:"children,omitempty"`
	// RemainOnExit keeps this pane open after its command exits.
	RemainOnExit *bool `toml:"remain_on_exit,omitempty"`
	// Zoomed starts this split focused and zoomed.
	Zoomed *bool `toml:"zoomed,omitempty"`
	// Zoom is an alias for Zoomed.
	Zoom *bool `toml:"zoom,omitempty"`
}

// Pane is one entry in the legacy flat pane list.
type Pane struct {
	Command string `toml:"command,omitempty"`
}

// Discover returns the config file to use when none was given: DefaultFileName
// in the current directory, falling back to LegacyFileName, or searching upward
// through parent directories up to a git repository root or user home directory.
func Discover() (string, error) {
	return DiscoverIn(".", true)
}

// DiscoverIn searches for a config file starting at startDir. If upward is true,
// it walks upward through parent directories until it finds a config, reaches
// a git repository boundary (.git), reaches the user home directory, or hits the
// filesystem root. A config found on the way that another user could have
// written is an error, not a match — see checkDiscoverable.
func DiscoverIn(startDir string, upward bool) (string, error) {
	dir := startDir
	if dir == "" || dir == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = cwd
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		homeDir, _ = filepath.Abs(homeDir)
	}

	current := absDir
	for {
		for _, name := range []string{DefaultFileName, LegacyFileName} {
			target := filepath.Join(current, name)
			if _, err := os.Stat(target); err == nil {
				if err := checkDiscoverable(current, target); err != nil {
					return "", err
				}
				if current == absDir {
					return name, nil
				}
				return target, nil
			}
		}

		if !upward {
			break
		}

		if isGitRoot(current) {
			break
		}
		if homeDir != "" && current == homeDir {
			break
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return "", fmt.Errorf("no %s or %s in the current directory (or pass -config)", DefaultFileName, LegacyFileName)
}

func isGitRoot(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	_, err := os.Stat(gitPath)
	return err == nil
}

// Load reads, parses, and validates a config file.
func Load(path string) (*Config, error) {
	// Not a silent fallback: cfg.dir is what a relative session.root resolves
	// against, so losing it doesn't degrade gracefully — it quietly roots the
	// session wherever the process happens to be standing.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", path, err)
	}
	hash := sha256.New()
	cfg, unknown, err := loadChain(abs, hash, nil)
	if err != nil {
		return nil, err
	}
	cfg.dir = filepath.Dir(abs)
	cfg.path = abs
	cfg.digest = hex.EncodeToString(hash.Sum(nil))
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, key := range unknown {
		cfg.warnings = append(cfg.warnings, fmt.Sprintf(
			"unknown key %s — it is ignored (a typo?)", key))
	}
	if filepath.Base(path) == LegacyFileName {
		cfg.warnings = append(cfg.warnings, fmt.Sprintf(
			"%s is deprecated and will be removed in 1.0; rename it to %s", LegacyFileName, DefaultFileName))
	}
	return cfg, nil
}

// Decode parses TOML into a Config and reports any keys the file sets that
// Config has no field for.
//
// Unknown keys are collected rather than rejected. A misspelled key is silently
// dropped by a plain Unmarshal, so `wyrm validate` would bless a config whose
// every key was a typo — the exact mistake validate exists to catch. But
// erroring outright would break configs that already carry stray keys, so for
// now they surface through Warnings (which `wyrm validate -strict` turns into a
// non-zero exit) and become errors in 1.0, alongside the other deprecations.
//
// go-toml still fills the destination when it reports strict errors, so the
// returned Config is complete either way.
func Decode(data []byte) (*Config, []string, error) {
	return decode(data)
}

func decode(data []byte) (*Config, []string, error) {
	var cfg Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var missing *toml.StrictMissingError
		if !errors.As(err, &missing) {
			return nil, nil, err
		}
		return &cfg, UnknownKeys(missing), nil
	}
	return &cfg, nil, nil
}

// UnknownKeys renders the dotted key paths go-toml rejected under
// DisallowUnknownFields, quoted, for an error or warning message. Shared with
// the TUI's theme loader so the two report a typo the same way.
func UnknownKeys(e *toml.StrictMissingError) []string {
	keys := make([]string, 0, len(e.Errors))
	for _, d := range e.Errors {
		keys = append(keys, strconv.Quote(strings.Join(d.Key(), ".")))
	}
	return keys
}

// LoadDefault parses and validates the built-in fallback config, used when no
// config file is found in the current directory (see Discover).
func LoadDefault() (*Config, error) {
	// Strict, and an outright error rather than a warning: the built-in config
	// is ours, so an unknown key in it is a wyrm bug, not a user's typo.
	cfg, unknown, err := decode(defaultConfigData)
	if err != nil {
		return nil, fmt.Errorf("parsing default config: %w", err)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("default config: unknown key %s", strings.Join(unknown, ", "))
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("default config: %w", err)
	}
	return cfg, nil
}

// Validate checks that the config has a valid session and window structure.
func (c *Config) Validate() error {
	return c.validate()
}

func (c *Config) validate() error {
	if c.Session.Name == "" && c.Session.Root == "" {
		return errors.New("config must set session.name or session.root")
	}
	// session.Create refuses a config with no windows, so rejecting it here
	// keeps `wyrm validate` from blessing a config `wyrm` will not build.
	if len(c.Windows) == 0 {
		return errors.New("config defines no windows (add at least one [[windows]])")
	}
	switch c.Session.PaneTitlePosition {
	case "", "top", "bottom":
	default:
		return fmt.Errorf("session.pane_title_position must be %q or %q, got %q", "top", "bottom", c.Session.PaneTitlePosition)
	}
	if err := validateEnv("session.env", c.Session.Env); err != nil {
		return err
	}
	if _, err := c.Session.HookTimeoutDuration(); err != nil {
		return err
	}
	for _, w := range c.Windows {
		if err := validateEnv(fmt.Sprintf("window %q env", w.Name), w.Env); err != nil {
			return err
		}
		if err := validateSplits(w.Name, w.Splits); err != nil {
			return err
		}
		c.warnings = append(c.warnings, windowWarnings(w)...)
	}
	return nil
}

func validateEnv(where string, env map[string]string) error {
	for k := range env {
		if k == "" {
			return fmt.Errorf("%s: empty environment variable name", where)
		}
		if strings.Contains(k, "=") || strings.Contains(k, "\x00") {
			return fmt.Errorf("%s: invalid environment variable name %q", where, k)
		}
	}
	return nil
}

// windowWarnings reports config that parses and builds but almost certainly
// doesn't do what its author meant.
func windowWarnings(w Window) []string {
	var out []string
	name := w.Name
	if name == "" {
		name = "(unnamed)"
	}
	if len(w.Splits) > 0 && len(w.Panes) > 0 {
		out = append(out, fmt.Sprintf("window %q sets both splits and panes; panes is ignored", name))
	}
	if len(w.Splits) > 0 && w.Layout != "" {
		// Applying a named tmux layout would discard the split tree's sizes,
		// so wyrm doesn't — but silently ignoring the key looks like a bug.
		out = append(out, fmt.Sprintf(
			"window %q sets layout=%q alongside splits; layout only applies to the legacy panes list and is ignored here", name, w.Layout))
	}
	if len(w.Panes) > 0 {
		out = append(out, fmt.Sprintf(
			"window %q uses the flat panes list, which is deprecated and will be removed in 1.0; use splits", name))
	}
	if len(w.Splits) > 0 && w.Splits[0].Type != "" {
		out = append(out, fmt.Sprintf(
			"window %q: the first split sets type=%q, so it splits the window's initial pane and leaves it empty — drop the type to put this entry in the initial pane", name, w.Splits[0].Type))
	}
	if w.SynchronizePanes != nil {
		out = append(out, fmt.Sprintf(
			"window %q: synchronize_panes is a deprecated alias and will be removed in 1.0; use synchronize", name))
	}
	out = append(out, splitWarnings(name, w.Splits)...)
	return out
}

// splitWarnings reports deprecated or ineffective keys anywhere in a split tree.
func splitWarnings(window string, splits []Split) []string {
	var out []string
	for i, s := range splits {
		if s.Zoom != nil {
			out = append(out, fmt.Sprintf(
				"window %q split %d: zoom is a deprecated alias and will be removed in 1.0; use zoomed", window, i))
		}
		if s.WaitFor != "" && s.Command == "" && s.Run == "" {
			out = append(out, fmt.Sprintf(
				"window %q split %d: wait_for has no command or run to delay, so it does nothing", window, i))
		}
		out = append(out, splitWarnings(window, s.Children)...)
	}
	return out
}

// HookTimeoutDuration parses hook_timeout. Zero means no limit.
func (s Session) HookTimeoutDuration() (time.Duration, error) {
	return parseDuration("session.hook_timeout", s.HookTimeout)
}

// WaitTimeoutDuration parses wait_timeout. Zero means wait.DefaultTimeout.
func (s Split) WaitTimeoutDuration() (time.Duration, error) {
	return parseDuration("wait_timeout", s.WaitTimeout)
}

func parseDuration(key, v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration (try \"90s\" or \"5m\")", key, v)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: %q must not be negative", key, v)
	}
	return d, nil
}

// Commands lists every piece of shell this config would execute, labelled by
// where it comes from. Hooks run as subprocesses and pane commands are typed
// into a shell; both execute, so both are listed. A config with none needs no
// trust decision — see internal/trust.
func (c *Config) Commands() []string {
	var out []string
	add := func(label, cmd string) {
		if cmd != "" {
			out = append(out, label+" = "+cmd)
		}
	}
	add("on_project_start", c.Session.OnProjectStart)
	add("on_project_first_start", c.Session.OnProjectFirstStart)
	add("on_project_restart", c.Session.OnProjectRestart)
	add("on_project_attach", c.Session.OnProjectAttach)
	add("on_project_exit", c.Session.OnProjectExit)
	add("on_project_detach", c.Session.OnProjectDetach)
	if c.Session.EnablePaneTitles != nil && *c.Session.EnablePaneTitles && strings.Contains(c.Session.PaneTitleFormat, "#(") {
		add("pane_title_format (tmux command substitution)", c.Session.PaneTitleFormat)
	}
	for _, w := range c.Windows {
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

func splitCommands(where string, splits []Split) []string {
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

func validateSplits(window string, splits []Split) error {
	for i, s := range splits {
		if err := validateEnv(fmt.Sprintf("window %q split %d env", window, i), s.Env); err != nil {
			return err
		}
		// 0 means "let tmux decide", so it's accepted alongside 1-99.
		if s.Size < 0 || s.Size > 99 {
			return fmt.Errorf("window %q split %d: size must be 1-99 (or omitted for tmux's default), got %d", window, i, s.Size)
		}
		switch strings.ToLower(s.Type) {
		case "", "h", "horizontal", "v", "vertical":
		default:
			return fmt.Errorf("window %q split %d: unknown type %q (use h/horizontal or v/vertical)", window, i, s.Type)
		}
		// Refusing rather than warning: the two do materially different things
		// (a pane with a shell under it, or without one), and silently picking
		// either would surprise half the people who wrote both.
		if s.Command != "" && s.Run != "" {
			return fmt.Errorf("window %q split %d: set command or run, not both (command types into a shell; run replaces it)", window, i)
		}
		if s.WaitFor != "" {
			if _, err := wait.Parse(s.WaitFor); err != nil {
				return fmt.Errorf("window %q split %d: %w", window, i, err)
			}
		}
		if _, err := s.WaitTimeoutDuration(); err != nil {
			return fmt.Errorf("window %q split %d: %w", window, i, err)
		}
		if err := validateSplits(window, s.Children); err != nil {
			return err
		}
	}
	return nil
}

// ResolveEffective returns the config that would be used for this project,
// mirroring wyrm's normal discovery order: an explicit path, the discovered
// local or shared file, the user's default override, then the built-in
// default. It returns the resolved source alongside the config — a file
// path, or "built-in default" when none exists on disk. Unlike main's
// bare-`wyrm` flow, it never falls back to the interactive session picker.
func ResolveEffective(settings *Settings, explicitPath string) (*Config, string, error) {
	if explicitPath != "" {
		cfg, err := Load(explicitPath)
		if err != nil {
			return nil, "", err
		}
		return cfg, explicitPath, nil
	}

	if discovered, derr := DiscoverGlobal(settings); derr == nil {
		cfg, err := Load(discovered)
		if err != nil {
			return nil, "", err
		}
		return cfg, discovered, nil
	}

	cfg, err := LoadUserDefault()
	if err != nil {
		return nil, "", err
	}
	if cfg != nil {
		path, err := UserDefaultPath()
		if err != nil {
			return nil, "", err
		}
		return cfg, path, nil
	}

	cfg, err = LoadDefault()
	if err != nil {
		return nil, "", err
	}
	return cfg, "built-in default", nil
}

// Resolve returns the session name and absolute root directory, deriving the
// name from the root's basename when unset. Root supports "~" and $VAR
// expansion.
//
// A relative root resolves against baseDir — the directory the config was
// loaded from (see Config.Dir) — not against the process's working directory.
// That's what makes a config mean the same thing wherever wyrm runs from: the
// TUI's Projects panel and `wyrm <name>` both build sessions for configs that
// aren't in the current folder, and resolving "." against the cwd there rooted
// the session wherever the user happened to be standing. An empty baseDir
// falls back to the working directory, for a Config built in memory.
func (s Session) Resolve(baseDir string) (name, absRoot string, err error) {
	root, err := ExpandPath(s.Root)
	if err != nil {
		return "", "", fmt.Errorf("resolving session.root: %w", err)
	}
	if root == "" {
		root = "."
	}
	if !filepath.IsAbs(root) && baseDir != "" {
		root = filepath.Join(baseDir, root)
	}
	absRoot, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolving root %q: %w", s.Root, err)
	}
	name = s.Name
	if name == "" {
		if wtName, ok := mainRepoWorktreeName(absRoot); ok {
			name = wtName
		} else {
			name = filepath.Base(absRoot)
		}
	}
	return name, absRoot, nil
}

// ResolveRoot resolves a configured directory against a base: "" yields base
// unchanged, an absolute (or ~/$VAR-expanded absolute) path is taken as-is, and
// a relative one is joined onto base. It is what makes a window's root = "api"
// mean "api inside the session root" while root = "~/other" still escapes it.
func ResolveRoot(base, root string) (string, error) {
	if root == "" {
		return base, nil
	}
	expanded, err := ExpandPath(root)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}
	return filepath.Join(base, expanded), nil
}

// ExpandPath expands a leading "~" and any $VAR references in a configured
// path. An unset variable is an error rather than an empty string:
// os.ExpandEnv would quietly turn "$PROJECTS/api" into "/api" and root a
// session at a directory nobody asked for.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	var missing []string
	expanded := os.Expand(p, func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		missing = append(missing, "$"+key)
		return ""
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("undefined variable %s in %q", strings.Join(missing, ", "), p)
	}
	return ExpandTilde(expanded)
}

// ExpandTilde replaces a leading "~" or "~/" with the user's home directory.
// Shared by session.root and settings.shared_dir so wyrm's two path settings
// can't disagree about what "~" means — they did, and root silently produced a
// literal directory named "~".
func ExpandTilde(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// Interpolate replaces template placeholders in the config with values from vars,
// and merges vars into session.env so child shells and hooks also receive them.
//
// {{name}} is the placeholder. {{.name}}, {{var.name}}, {{.var.name}},
// ${name} and bare $name still substitute, but are deprecated and reported in
// the returned warnings: the last two collide with the shell's own variables,
// so a hook mentioning $HOME was rewritten whenever a --var happened to be
// called HOME. They are removed in 1.0.
func (c *Config) Interpolate(vars map[string]string) []string {
	if c == nil || len(vars) == 0 {
		return nil
	}
	ip := &interpolator{vars: vars}
	c.Session.Name = ip.str(c.Session.Name)
	c.Session.Root = ip.str(c.Session.Root)
	c.Session.OnProjectStart = ip.str(c.Session.OnProjectStart)
	c.Session.OnProjectExit = ip.str(c.Session.OnProjectExit)
	c.Session.OnProjectAttach = ip.str(c.Session.OnProjectAttach)
	c.Session.OnProjectDetach = ip.str(c.Session.OnProjectDetach)
	c.Session.OnProjectFirstStart = ip.str(c.Session.OnProjectFirstStart)
	c.Session.OnProjectRestart = ip.str(c.Session.OnProjectRestart)
	c.Session.StartupWindow = ip.str(c.Session.StartupWindow)
	c.Session.PaneTitleFormat = ip.str(c.Session.PaneTitleFormat)

	if c.Session.Env == nil {
		c.Session.Env = make(map[string]string)
	}
	for k, v := range c.Session.Env {
		c.Session.Env[k] = ip.str(v)
	}
	for k, v := range vars {
		if _, exists := c.Session.Env[k]; !exists {
			c.Session.Env[k] = v
		}
	}

	for i := range c.Windows {
		c.Windows[i].Name = ip.str(c.Windows[i].Name)
		c.Windows[i].Root = ip.str(c.Windows[i].Root)
		c.Windows[i].PreWindow = ip.str(c.Windows[i].PreWindow)
		c.Windows[i].PostWindow = ip.str(c.Windows[i].PostWindow)
		for k, v := range c.Windows[i].Env {
			c.Windows[i].Env[k] = ip.str(v)
		}
		ip.splits(c.Windows[i].Splits)
		for j := range c.Windows[i].Panes {
			c.Windows[i].Panes[j].Command = ip.str(c.Windows[i].Panes[j].Command)
		}
	}
	return ip.warnings()
}

func (ip *interpolator) splits(splits []Split) {
	for i := range splits {
		splits[i].Command = ip.str(splits[i].Command)
		splits[i].Run = ip.str(splits[i].Run)
		splits[i].Root = ip.str(splits[i].Root)
		for k, v := range splits[i].Env {
			splits[i].Env[k] = ip.str(v)
		}
		ip.splits(splits[i].Children)
	}
}

// InterpolateString performs deterministic, boundary-safe template variable
// substitution, accepting every placeholder form Interpolate does.
func InterpolateString(s string, vars map[string]string) string {
	return (&interpolator{vars: vars}).str(s)
}

// interpolator substitutes vars and remembers which deprecated placeholder
// forms it substituted through.
type interpolator struct {
	vars map[string]string
	used map[string]bool
}

func (ip *interpolator) warnings() []string {
	forms := make([]string, 0, len(ip.used))
	for f := range ip.used {
		forms = append(forms, f)
	}
	sort.Strings(forms)
	out := make([]string, 0, len(forms))
	for _, f := range forms {
		out = append(out, fmt.Sprintf("placeholder %s is deprecated and will be removed in 1.0; write {{name}}", f))
	}
	return out
}

func (ip *interpolator) note(form, before, after string) {
	if before == after {
		return
	}
	if ip.used == nil {
		ip.used = map[string]bool{}
	}
	ip.used[form] = true
}

// str substitutes every var in s. Keys are applied longest first, then
// alphabetically, so the result is deterministic and a longer name is never
// clobbered by a shorter prefix of it.
func (ip *interpolator) str(s string) string {
	if s == "" || len(ip.vars) == 0 {
		return s
	}
	keys := make([]string, 0, len(ip.vars))
	for k := range ip.vars {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})

	res := s
	for _, k := range keys {
		v := ip.vars[k]
		for _, form := range []struct{ placeholder, shown string }{
			{"{{.var." + k + "}}", "{{.var.name}}"},
			{"{{var." + k + "}}", "{{var.name}}"},
			{"{{." + k + "}}", "{{.name}}"},
		} {
			next := strings.ReplaceAll(res, form.placeholder, v)
			ip.note(form.shown, res, next)
			res = next
		}
		res = strings.ReplaceAll(res, "{{"+k+"}}", v)
		next := strings.ReplaceAll(res, "${"+k+"}", v)
		ip.note("${name}", res, next)
		res = next
		next = replaceBareVar(res, k, v)
		ip.note("$name", res, next)
		res = next
	}
	return res
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func replaceBareVar(s, k, v string) string {
	target := "$" + k
	if !strings.Contains(s, target) {
		return s
	}
	var sb strings.Builder
	targetLen := len(target)
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], target)
		if idx == -1 {
			sb.WriteString(s[i:])
			break
		}
		pos := i + idx
		sb.WriteString(s[i:pos])
		nextIdx := pos + targetLen
		if nextIdx < len(s) && isIdentChar(s[nextIdx]) {
			// Next character continues an identifier (e.g. $PORT_SSL when matching $PORT), so keep as is
			sb.WriteString(target)
		} else {
			sb.WriteString(v)
		}
		i = nextIdx
	}
	return sb.String()
}
