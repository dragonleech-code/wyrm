// Package trust records which project configs the user has agreed to run.
//
// A wyrm config executes shell by design, and most configs come from the user.
// The exception is a config that arrived with someone else's repository:
// `git clone x && cd x && wyrm` would otherwise run x's hooks without the user
// ever having read them. Before running a config from outside the user's own
// wyrm directory, callers check it here; an unknown or changed config is shown
// to the user and only recorded as trusted once they agree.
//
// A decision is keyed by the config's absolute path and bound to its
// fingerprint (see config.Config.Fingerprint), so any edit to the config — or
// to a base it extends — asks again.
package trust

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/state"
	"github.com/pelletier/go-toml/v2"
)

// FileName is the trust store, kept alongside wyrm's settings.
const FileName = "trust.toml"

// EnvTrustAll names the environment variable that, set to "1", trusts every
// config without asking. It is for scripts and CI, where there is no one to
// ask.
const EnvTrustAll = "WYRM_TRUST_ALL"

// Status is the outcome of checking a config against the store.
type Status int

const (
	// Unknown means the config has never been allowed or denied.
	Unknown Status = iota
	// Trusted means the config was allowed with this exact fingerprint.
	Trusted
	// Changed means the config was allowed, but has been edited since.
	Changed
	// Denied means the user refused this config.
	Denied
)

func (s Status) String() string {
	switch s {
	case Trusted:
		return "trusted"
	case Changed:
		return "changed since trusted"
	case Denied:
		return "denied"
	}
	return "not trusted"
}

// Entry is one recorded decision.
type Entry struct {
	Path   string    `toml:"path"`
	SHA256 string    `toml:"sha256,omitempty"`
	Denied bool      `toml:"denied,omitempty"`
	At     time.Time `toml:"at"`
}

type fileFormat struct {
	Configs []Entry `toml:"config"`
}

// Store is the loaded trust file.
type Store struct {
	path    string
	entries map[string]Entry
}

// Load reads the trust store, returning an empty one when the file does not
// exist yet.
func Load() (*Store, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	return LoadFrom(filepath.Join(dir, "wyrm", FileName))
}

// LoadFrom reads the trust store at path.
func LoadFrom(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]Entry{}}
	return s, s.read()
}

func (s *Store) read() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ff fileFormat
	if err := toml.Unmarshal(data, &ff); err != nil {
		return fmt.Errorf("parsing %s: %w", s.path, err)
	}
	for _, e := range ff.Configs {
		s.entries[e.Path] = e
	}
	return nil
}

// Path is the file this store persists to.
func (s *Store) Path() string { return s.path }

// Check reports what the store says about the config at path with the given
// fingerprint.
func (s *Store) Check(path, fingerprint string) Status {
	e, ok := s.entries[path]
	switch {
	case !ok:
		return Unknown
	case e.Denied:
		return Denied
	case e.SHA256 == fingerprint:
		return Trusted
	}
	return Changed
}

// Allow records path as trusted at fingerprint and saves.
func (s *Store) Allow(path, fingerprint string) error {
	return s.update(Entry{Path: path, SHA256: fingerprint, At: time.Now().UTC()})
}

// Deny records path as refused and saves. A denied config is never run and
// never asked about again until it is allowed explicitly.
func (s *Store) Deny(path string) error {
	return s.update(Entry{Path: path, Denied: true, At: time.Now().UTC()})
}

// Forget removes any decision about path and saves.
func (s *Store) Forget(path string) error {
	return s.update(Entry{Path: path})
}

// Entries returns every recorded decision, sorted by path.
func (s *Store) Entries() []Entry {
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// update re-reads the file, applies e (an Entry with neither a fingerprint
// nor Denied removes the path), and writes it back, all under a lock so two
// wyrm processes can't erase each other's decisions.
func (s *Store) update(e Entry) error {
	unlock, err := state.LockFile(s.path)
	if err != nil {
		return fmt.Errorf("locking %s: %w", s.path, err)
	}
	defer unlock()
	s.entries = map[string]Entry{}
	if err := s.read(); err != nil {
		return err
	}
	if e.SHA256 == "" && !e.Denied {
		delete(s.entries, e.Path)
	} else {
		s.entries[e.Path] = e
	}
	data, err := toml.Marshal(fileFormat{Configs: s.Entries()})
	if err != nil {
		return err
	}
	// 0600: the file lists which project directories exist on this machine.
	return state.AtomicWriteFile(s.path, data, 0o600)
}

func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// Required reports whether running cfg needs a trust decision at all. It does
// not when trust is turned off, when cfg did not come from a file (the
// built-in default), when it runs no commands, or when it lives somewhere only
// the user's own configuration puts files (see config.Settings.OwnedPath).
func Required(settings *config.Settings, cfg *config.Config) bool {
	if os.Getenv(EnvTrustAll) == "1" || !settings.TrustEnabled() {
		return false
	}
	if cfg == nil || cfg.Path() == "" || len(cfg.Commands()) == 0 {
		return false
	}
	return !settings.OwnedPath(cfg.Path())
}
