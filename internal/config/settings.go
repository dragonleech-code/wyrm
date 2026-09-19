package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SettingsFileName is wyrm's global, cross-project preferences file.
const SettingsFileName = "config.toml"

// UserDefaultFileName is a user-supplied replacement for the built-in
// default config, stored alongside SettingsFileName.
const UserDefaultFileName = "default.wyrm.toml"

// ThemeFileName is the TUI's optional color override, stored alongside
// SettingsFileName.
const ThemeFileName = "theme.toml"

// DefaultSharedDir is the shared config directory used when
// Settings.SharedDir is unset, for documentation and error messages. The
// resolved path comes from defaultSharedDir, which honors $XDG_CONFIG_HOME.
const DefaultSharedDir = "~/.config/wyrm/settings"

// defaultSharedDir returns the shared config directory to use when none is
// configured: alongside the settings file, so it follows $XDG_CONFIG_HOME the
// same way SettingsPath does. Hardcoding "~/.config" meant a user with
// XDG_CONFIG_HOME set had their settings read from one place and their shared
// configs looked for in another.
func defaultSharedDir() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wyrm", "settings"), nil
}

// Storage selects where wyrm looks for a project's config file.
type Storage string

const (
	// StorageLocal (the default) looks for DefaultFileName/LegacyFileName in
	// the current directory, as wyrm always has.
	StorageLocal Storage = "local"
	// StorageShared looks for "<folderName>.wyrm.toml" in the shared config
	// directory first, falling back to StorageLocal behavior if it's absent.
	StorageShared Storage = "shared"
)

// Settings is wyrm's global preferences, shared across all projects.
type Settings struct {
	Storage   Storage           `toml:"storage"`
	SharedDir string            `toml:"shared_dir"`
	TUI       TUI               `toml:"tui"`
	Tmux      Tmux              `toml:"tmux"`
	Wildcard  []Wildcard        `toml:"wildcard,omitempty"`
	Zoxide    Zoxide            `toml:"zoxide"`
	Discovery DiscoverySettings `toml:"discovery"`

	// warnings collects unknown top-level keys found while parsing this
	// settings file (see LoadSettings), mirroring Config.warnings.
	warnings []string
}

// DiscoverySettings configures config file discovery options.
type DiscoverySettings struct {
	// Upward enables traversing up parent directories to find .wyrm.toml.
	// Defaults to true.
	Upward *bool `toml:"upward"`
}

// UpwardDiscoveryEnabled returns true unless discovery.upward is explicitly false.
func (s *Settings) UpwardDiscoveryEnabled() bool {
	if s == nil || s.Discovery.Upward == nil {
		return true
	}
	return *s.Discovery.Upward
}

// Zoxide configures the TUI's optional zoxide-backed directory discovery —
// see internal/zoxide and ZoxideEnabled/ZoxideTrack. Both fields default to
// false, unlike TUI.Mouse/TUI.Agent.Enabled (which default true): those are
// pure UI conveniences with no external dependency, while this one is a
// real dependency on a binary other than tmux and a side-effecting write
// into zoxide's own database, so it must not activate just because zoxide
// happens to be installed.
type Zoxide struct {
	// Enabled shows zoxide-known directories (that don't already have a
	// wyrm project) in the TUI's Projects panel.
	Enabled *bool `toml:"enabled"`
	// Track calls `zoxide add` after building a session, so using wyrm to
	// reach a directory also teaches zoxide about it.
	Track *bool `toml:"track"`
}

// Wildcard applies one config as a template to every directory matching
// Pattern, instead of requiring a [[session]]-style config per directory.
// See DiscoverWildcardProjects.
type Wildcard struct {
	// Pattern is a glob (Go's filepath.Match syntax: "*", "?", "[...]"),
	// optionally ending in "/**" for recursive matching. "~" and $VAR are
	// expanded.
	Pattern string `toml:"pattern"`
	// Config is the template config file applied to every directory Pattern
	// matches — an ordinary .wyrm.toml whose session.name is normally left
	// unset, since the matched directory supplies it. session.root, on the
	// other hand, always gets overridden with the matched directory
	// regardless of what the file says — but Load still requires the file to
	// set *something* there (or a name), so a template conventionally sets
	// root = "." as a placeholder (see DiscoverWildcardProjects).
	Config string `toml:"config"`
}

// Warnings returns this settings file's non-fatal problems: unknown keys, one
// per offending dotted path. Unlike Config.Warnings, there's no -strict
// consumer for these yet — they're printed once, by main, right after the
// initial load.
func (s *Settings) Warnings() []string {
	if s == nil {
		return nil
	}
	return s.warnings
}

// Tmux configures which tmux binary and server wyrm talks to. Both fields can
// also be set via WYRM_TMUX_COMMAND / WYRM_TMUX_SOCKET, which take priority —
// see Settings.TmuxCommand / TmuxSocket.
type Tmux struct {
	// Socket selects a separate tmux server (tmux -L). Empty uses the default
	// server.
	Socket string `toml:"socket"`
	// Command overrides the binary invoked in place of "tmux" — a full path,
	// or a wrapper/fork like "byobu" or "psmux". Empty resolves "tmux" from
	// PATH.
	Command string `toml:"command"`
}

// TmuxSocket returns the tmux -L socket name to use: WYRM_TMUX_SOCKET if set,
// else [tmux].socket, else "" (the default server). Nil-safe.
func (s *Settings) TmuxSocket() string {
	if v := os.Getenv("WYRM_TMUX_SOCKET"); v != "" {
		return v
	}
	if s == nil {
		return ""
	}
	return s.Tmux.Socket
}

// TmuxCommand returns the tmux binary to invoke: WYRM_TMUX_COMMAND if set,
// else [tmux].command, else "" (resolve "tmux" from PATH). Nil-safe.
func (s *Settings) TmuxCommand() string {
	if v := os.Getenv("WYRM_TMUX_COMMAND"); v != "" {
		return v
	}
	if s == nil {
		return ""
	}
	return s.Tmux.Command
}

// TUI holds the interactive session manager's preferences.
//
// The bool fields are pointers so "absent" and "explicitly false" stay
// distinguishable: both of these default to on, and a plain bool would make an
// unwritten settings file indistinguishable from one that turned them off.
type TUI struct {
	// Mouse enables mouse reporting in the TUI. Defaults to true; a user who
	// would rather keep their terminal's own click-drag text selection can set
	// it to false here, or toggle it for one run with "m".
	Mouse *bool `toml:"mouse"`
	Agent Agent `toml:"agent"`
}

// Agent configures the "this pane is waiting for you" markers and notifications.
type Agent struct {
	// Enabled turns agent detection on. Defaults to true. Turning it off also
	// stops the pane captures it costs.
	Enabled *bool `toml:"enabled"`
	// Commands are the #{pane_current_command} values treated as an agent pane.
	// Empty means the built-in default (claude).
	//
	// It only widens which panes are inspected; the patterns that classify them
	// stay the built-in ones. Use Profiles to describe a different agent.
	Commands []string `toml:"commands"`
	// Profiles describe agents wyrm doesn't ship knowing about: which command
	// each runs as, and the on-screen chrome that marks it busy, blocked, or
	// idle. A non-empty list replaces the built-in profile entirely rather than
	// adding to it — otherwise one agent's chrome could decide another's state.
	Profiles []AgentProfile `toml:"profiles"`
	// Notify configures terminal and desktop notifications when an agent changes state.
	Notify AgentNotify `toml:"notify"`
}

// AgentNotify configures notification delivery for agent state changes.
type AgentNotify struct {
	// Enabled turns agent notifications on. Defaults to false.
	Enabled *bool `toml:"enabled"`
	// Desktop enables OS desktop notifications (osascript on macOS, notify-send on Linux). Defaults to true if notify is enabled.
	Desktop *bool `toml:"desktop"`
	// Bell enables terminal bell escape sequence (\a). Defaults to false.
	Bell *bool `toml:"bell"`
	// OSC enables terminal OSC 9 / OSC 777 notification escape sequences. Defaults to false.
	OSC *bool `toml:"osc"`
	// OnBlocked triggers notification when an agent transitions to blocked. Defaults to true.
	OnBlocked *bool `toml:"on_blocked"`
	// OnIdle triggers notification when an agent transitions to idle. Defaults to false.
	OnIdle *bool `toml:"on_idle"`
	// Command is an optional shell command to execute for notifications. It
	// runs via $SHELL with the notification in the environment as
	// WYRM_NOTIFY_TITLE, _MESSAGE, _STATE, _SESSION and _PANE — see
	// agent.BuildCustomNotifyCommand. This comment previously promised
	// {title}/{message}/{state}/{session}/{pane} placeholder expansion, which
	// nothing has ever implemented.
	//
	// Setting this replaces the desktop notification rather than adding to it:
	// agent.Dispatch returns once the command has run.
	Command string `toml:"command"`
}

// AgentProfile mirrors agent.Profile in the settings file. It is duplicated
// rather than imported so internal/config keeps no dependency on the detector,
// which is what lets internal/agent stay a leaf package.
type AgentProfile struct {
	Commands    []string `toml:"commands"`
	Busy        []string `toml:"busy"`
	Blocked     []string `toml:"blocked"`
	Idle        []string `toml:"idle"`
	BusyPattern string   `toml:"busy_pattern"`
}

// MouseEnabled reports whether the TUI should start with the mouse captured.
// Nil-safe: a nil Settings takes the defaults, which is how the TUI is
// constructed in tests and when no settings file exists.
func (s *Settings) MouseEnabled() bool {
	if s == nil || s.TUI.Mouse == nil {
		return true
	}
	return *s.TUI.Mouse
}

// AgentEnabled reports whether the TUI should look for waiting agent panes.
func (s *Settings) AgentEnabled() bool {
	if s == nil || s.TUI.Agent.Enabled == nil {
		return true
	}
	return *s.TUI.Agent.Enabled
}

// AgentNotifyEnabled reports whether agent state notifications are enabled.
func (s *Settings) AgentNotifyEnabled() bool {
	if s == nil || s.TUI.Agent.Notify.Enabled == nil {
		return false
	}
	return *s.TUI.Agent.Notify.Enabled
}

// AgentNotifyDesktop reports whether desktop notifications are enabled.
func (s *Settings) AgentNotifyDesktop() bool {
	if s == nil || s.TUI.Agent.Notify.Desktop == nil {
		return true
	}
	return *s.TUI.Agent.Notify.Desktop
}

// AgentNotifyBell reports whether terminal bell notifications are enabled.
func (s *Settings) AgentNotifyBell() bool {
	if s == nil || s.TUI.Agent.Notify.Bell == nil {
		return false
	}
	return *s.TUI.Agent.Notify.Bell
}

// AgentNotifyOSC reports whether OSC 9 / OSC 777 escape notifications are enabled.
func (s *Settings) AgentNotifyOSC() bool {
	if s == nil || s.TUI.Agent.Notify.OSC == nil {
		return false
	}
	return *s.TUI.Agent.Notify.OSC
}

// AgentNotifyOnBlocked reports whether notifications fire when an agent becomes blocked.
func (s *Settings) AgentNotifyOnBlocked() bool {
	if s == nil || s.TUI.Agent.Notify.OnBlocked == nil {
		return true
	}
	return *s.TUI.Agent.Notify.OnBlocked
}

// AgentNotifyOnIdle reports whether notifications fire when an agent becomes idle.
func (s *Settings) AgentNotifyOnIdle() bool {
	if s == nil || s.TUI.Agent.Notify.OnIdle == nil {
		return false
	}
	return *s.TUI.Agent.Notify.OnIdle
}

// AgentNotifyCommand returns the custom notification shell command template.
func (s *Settings) AgentNotifyCommand() string {
	if s == nil {
		return ""
	}
	return s.TUI.Agent.Notify.Command
}

// ZoxideEnabled reports whether the TUI should list zoxide-known
// directories in the Projects panel. Defaults to false — see Zoxide.
func (s *Settings) ZoxideEnabled() bool {
	if s == nil || s.Zoxide.Enabled == nil {
		return false
	}
	return *s.Zoxide.Enabled
}

// ZoxideTrack reports whether wyrm should call `zoxide add` after building a
// session. Defaults to false.
func (s *Settings) ZoxideTrack() bool {
	if s == nil || s.Zoxide.Track == nil {
		return false
	}
	return *s.Zoxide.Track
}

// AgentCommands returns the pane commands to treat as agents; nil means the
// package default.
func (s *Settings) AgentCommands() []string {
	if s == nil {
		return nil
	}
	return s.TUI.Agent.Commands
}

// AgentProfiles returns the configured agent profiles; nil means the built-in
// one. A bare `commands` list is surfaced here as a profile carrying the
// built-in patterns, so the two settings compose the way a reader would expect:
// commands widens what the shipped detector looks at, profiles replaces it.
func (s *Settings) AgentProfiles() []AgentProfile {
	if s == nil {
		return nil
	}
	return s.TUI.Agent.Profiles
}

// SettingsPath returns the path to the global settings file, honoring
// $XDG_CONFIG_HOME and falling back to ~/.config.
func SettingsPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wyrm", SettingsFileName), nil
}

// ThemePath returns the path to the TUI's optional color override file,
// honoring $XDG_CONFIG_HOME and falling back to ~/.config. The file is read by
// internal/tui; config only owns where it lives, alongside the other
// user-level files.
func ThemePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wyrm", ThemeFileName), nil
}

// UserDefaultPath returns the path to the user's default config override,
// honoring $XDG_CONFIG_HOME and falling back to ~/.config.
func UserDefaultPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wyrm", UserDefaultFileName), nil
}

// LoadUserDefault reads, parses, and validates the user's default config
// override (see UserDefaultPath). It returns a nil Config, with no error,
// when no override file exists — callers should then fall back to
// LoadDefault.
func LoadUserDefault() (*Config, error) {
	path, err := UserDefaultPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	// This override is a template meant to apply wherever `wyrm up` happens to
	// run, not a project config of its own — so a relative session.root
	// (".", above all) has to resolve against the invocation directory, not
	// against ~/.config/wyrm where the file itself lives. Load set cfg.dir to
	// the latter; this is the one place that knows the two differ for this
	// particular config.
	if cwd, err := os.Getwd(); err == nil {
		cfg.dir = cwd
	}
	return cfg, nil
}

func configDir() (string, error) { return UserConfigDir() }

// UserConfigDir is the base configuration directory: $XDG_CONFIG_HOME when
// set, else ~/.config.
//
// Exported because it is not only wyrm's own config that lives under it.
// `wyrm setup-tmux -a` looks for the user's tmux.conf and had this rule
// hardcoded to ~/.config, so anyone with XDG_CONFIG_HOME pointed elsewhere had
// the snippet written to a file tmux does not read — silently, and reporting
// success.
func UserConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// LoadSettings reads the global settings file, returning defaults
// (StorageLocal) when it doesn't exist.
//
// Parsing is strict (DisallowUnknownFields), the same as a project config
// (see decode): a misspelled key here used to be silently dropped, which
// meant e.g. "[[widcard]]" would parse clean and just never take effect. The
// resulting Settings.Warnings() are printed once, by main, rather than at
// every one of this function's call sites.
func LoadSettings() (*Settings, error) {
	path, err := SettingsPath()
	if err != nil {
		return nil, err
	}
	s := &Settings{Storage: StorageLocal}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(s); err != nil {
		var missing *toml.StrictMissingError
		if !errors.As(err, &missing) {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		for _, key := range UnknownKeys(missing) {
			s.warnings = append(s.warnings, fmt.Sprintf("unknown key %s — it is ignored (a typo?)", key))
		}
	}
	if s.Storage == "" {
		s.Storage = StorageLocal
	}
	if s.Storage != StorageLocal && s.Storage != StorageShared {
		return nil, fmt.Errorf("%s: storage must be %q or %q, got %q", path, StorageLocal, StorageShared, s.Storage)
	}
	return s, nil
}

// ResolvedSharedDir returns the absolute shared config directory, expanding
// "~" and $VARS and defaulting to DefaultSharedDir when unset.
func (s *Settings) ResolvedSharedDir() (string, error) {
	if s.SharedDir == "" {
		dir, err := defaultSharedDir()
		if err != nil {
			return "", err
		}
		return filepath.Abs(dir)
	}
	dir, err := ExpandPath(s.SharedDir)
	if err != nil {
		return "", fmt.Errorf("resolving shared_dir: %w", err)
	}
	return filepath.Abs(dir)
}

// SharedConfigPath returns the path to the shared config file for the project
// rooted at dir: "<folderName>.wyrm.toml" inside the shared config directory,
// or "<folderName>-<hash>.wyrm.toml" when the plain name is already taken by a
// different project.
//
// The plain name alone collides on basename, which monorepos make ordinary:
// ~/work/api and ~/personal/api, or services/api and packages/api, all map to
// api.wyrm.toml. That made the second project silently read the first one's
// config and build the wrong session in the wrong root, because DiscoverGlobal
// and migrate-config both ask this function where the file lives.
//
// Disambiguation is deliberately conditional rather than unconditional: an
// existing single project keeps the name it already has on disk, so nothing
// needs migrating. Only the second project to claim a basename gets a suffix.
func (s *Settings) SharedConfigPath(dir string) (string, error) {
	sharedDir, err := s.ResolvedSharedDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	plain := filepath.Join(sharedDir, filepath.Base(abs)+DefaultFileName)
	if owner, known := SharedConfigOwner(plain); known && !SamePath(owner, abs) {
		return filepath.Join(sharedDir, filepath.Base(abs)+"-"+shortPathHash(canonicalPath(abs))+DefaultFileName), nil
	}
	return plain, nil
}

// SamePath reports whether two paths name the same directory, comparing what
// they resolve to rather than how they are spelled.
//
// A plain string comparison is not enough: os.Getwd returns a symlink-resolved
// path while a config's session.root is whatever the user typed, so on macOS
// (/var -> /private/var) or any setup with a symlinked home, a project failed
// to recognise its own shared config and fell through to discovery.
//
// Exported because every comparison against SharedConfigOwner needs it —
// `wyrm migrate-config` used == and could therefore tell you a file belonged
// to another project when it was your own, spelled differently.
func SamePath(a, b string) bool {
	return a == b || canonicalPath(a) == canonicalPath(b)
}

// canonicalPath resolves symlinks where it can, and otherwise returns the path
// unchanged — a path that does not exist yet cannot be resolved, and is still
// perfectly usable as an identity.
func canonicalPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// SharedConfigOwner reports the absolute project directory a shared config
// belongs to, and whether that could be determined at all.
//
// Only an absolute session.root identifies a project. A missing root, or a
// relative one — which resolves against the shared directory and is what
// CheckSharedRoot warns about — is reported as unknown, and every caller
// treats unknown as "assume it is ours". That is what keeps configs written
// before this existed working exactly as they did.
func SharedConfigOwner(path string) (string, bool) {
	cfg, err := Load(path)
	if err != nil || cfg == nil {
		return "", false
	}
	if cfg.Session.ProjectDir != "" {
		dir, err := ExpandPath(cfg.Session.ProjectDir)
		return dir, err == nil && filepath.IsAbs(dir)
	}
	root := cfg.Session.Root
	if root == "" {
		return "", false
	}
	if !filepath.IsAbs(root) && !strings.HasPrefix(root, "~") && !strings.Contains(root, "$") {
		return "", false
	}
	_, resolved, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", false
	}
	return abs, true
}

// shortPathHash is the disambiguator in a shared config filename: eight hex
// characters of the project's absolute path. Short enough to keep the filename
// readable, and stable, so the same project always resolves to the same file.
func shortPathHash(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:4])
}

// EditTarget returns the path wyrm edit should open: the discovered config
// if one exists, otherwise the path a new one should be created at per
// settings.Storage — the shared path (mirroring -migrate-config's
// destination) in shared mode, DefaultFileName in the cwd otherwise.
func EditTarget(settings *Settings) (path string, exists bool, err error) {
	if discovered, derr := DiscoverGlobal(settings); derr == nil {
		return discovered, true, nil
	}
	if settings != nil && settings.Storage == StorageShared {
		cwd, err := os.Getwd()
		if err != nil {
			return "", false, err
		}
		path, err := settings.SharedConfigPath(cwd)
		if err != nil {
			return "", false, err
		}
		return path, false, nil
	}
	return DefaultFileName, false, nil
}

// DiscoverGlobal is like Discover, but honors settings.Storage: in
// StorageShared mode it looks for the shared "<folderName>.wyrm.toml" first,
// falling back to Discover's normal current-directory search if that file
// doesn't exist.
func DiscoverGlobal(settings *Settings) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if settings != nil && settings.Storage == StorageShared {
		shared, err := settings.SharedConfigPath(cwd)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(shared); err == nil {
			return shared, nil
		}
	}
	upward := true
	if settings != nil {
		upward = settings.UpwardDiscoveryEnabled()
	}
	return DiscoverIn(cwd, upward)
}
