// Package state persists small facts about wyrm that must survive past a
// single invocation and even past a `wyrm kill` — currently just which
// project directories have ever started a session, so a lifecycle hook can
// tell a project's genuine first start from a later restart.
//
// It has no dependency on internal/config or internal/session, so it can be
// reasoned about and tested entirely on its own; internal/session consumes
// it through a small interface (HookHistory) rather than importing this
// package directly.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// FileName is wyrm's small persistent-state file, stored alongside the
// global settings file.
const FileName = "state.toml"

// Store tracks which project directories have started a session before.
// The zero value is not usable — construct one with Load.
type Store struct {
	path    string
	started map[string]bool
}

type fileFormat struct {
	// Started is every project directory (absolute, as Config.Dir returns)
	// that has started a session at least once.
	Started []string `toml:"started"`
}

// Load reads the state file, returning an empty, usable Store — not an
// error — when it doesn't exist yet, the same tolerance LoadSettings gives a
// missing global settings file.
func Load() (*Store, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, started: map[string]bool{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var ff fileFormat
	if err := toml.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for _, d := range ff.Started {
		s.started[d] = true
	}
	return s, nil
}

// Path returns the file this store persists to, so callers can name it in a
// diagnostic without recomputing the XDG lookup.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Len returns how many project directories are on record as having started.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	return len(s.started)
}

// Started reports whether dir has started a session before. Nil-safe and
// false for an empty dir, so a Config with no on-disk identity (the
// built-in default, or one built in memory) never matches.
func (s *Store) Started(dir string) bool {
	if s == nil || dir == "" {
		return false
	}
	return s.started[dir]
}

// LockProject serializes the full startup lifecycle for one resolved root.
// Refreshing under this lock makes a concurrent process's completed start
// visible to a Store loaded before that process acquired the lock.
func (s *Store) LockProject(dir string) (func(), error) {
	if s == nil || dir == "" {
		return func() {}, nil
	}
	sum := sha256.Sum256([]byte(dir))
	unlock, err := lockFileWithTimeout(s.path+".start-"+hex.EncodeToString(sum[:8]), 10*time.Minute)
	if err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				_ = os.Chtimes(s.path+".start-"+hex.EncodeToString(sum[:8])+".lock", now, now)
			}
		}
	}()
	release := func() { close(stop); <-done; unlock() }
	if err := s.refresh(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (s *Store) refresh() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ff fileFormat
	if err := toml.Unmarshal(data, &ff); err != nil {
		return fmt.Errorf("reading project history: %w", err)
	}
	for _, dir := range ff.Started {
		s.started[dir] = true
	}
	return nil
}

// MarkStarted records dir as started and persists immediately — there is no
// separate Save, because a start recorded but not yet on disk is exactly
// the state a crash between the two would leave wrong.
//
// The write is serialised against other wyrm processes with a lockfile, and
// re-reads the file inside the lock before merging. Without that, two
// concurrent `wyrm up` runs in different directories both loaded the same
// starting set, and whichever wrote second erased the other's entry — so
// on_project_first_start fired a second time for a project that had already
// started, which is the one thing this file exists to prevent.
func (s *Store) MarkStarted(dir string) error {
	if s == nil || dir == "" {
		return nil
	}
	if s.started[dir] {
		return nil
	}
	unlock, err := lockFile(s.path)
	if err != nil {
		return fmt.Errorf("locking project history: %w", err)
	}
	defer unlock()

	// Re-read inside the lock and merge whatever landed while we waited.
	if data, rerr := os.ReadFile(s.path); rerr == nil {
		var ff fileFormat
		if err := toml.Unmarshal(data, &ff); err != nil {
			return fmt.Errorf("reading project history: %w", err)
		}
		for _, d := range ff.Started {
			s.started[d] = true
		}
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return fmt.Errorf("reading project history: %w", rerr)
	}
	s.started[dir] = true
	if err := s.save(); err != nil {
		delete(s.started, dir)
		return err
	}
	return nil
}

// How long MarkStarted waits for another wyrm process to finish its write.
//
// The critical section is a read, a marshal, and a synced rename — tens of
// milliseconds, and the fsync dominates. The budget has to cover every other
// waiter's turn, not just one: at 500ms, twelve concurrent starts left the last
// eight timing out and falling back. Five seconds is far past plausible
// contention for a file this small, so reaching it means a stale lock, which
// lockStaleAfter handles separately.
const (
	lockTimeout    = 5 * time.Second
	lockRetryDelay = 5 * time.Millisecond
	lockStaleAfter = 30 * time.Second
)

// lockFile takes an exclusive lock for path via an O_EXCL sibling file, and
// returns the function that releases it.
func lockFile(path string) (func(), error) {
	return lockFileWithTimeout(path, lockTimeout)
}

func lockFileWithTimeout(path string, timeout time.Duration) (func(), error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		// A process killed mid-write leaves the lock behind forever; break a
		// clearly abandoned one rather than making every later run wait.
		if info, serr := os.Stat(lock); serr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for " + lock)
		}
		time.Sleep(lockRetryDelay)
	}
}

func (s *Store) save() error {
	dirs := make([]string, 0, len(s.started))
	for d := range s.started {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	data, err := toml.Marshal(fileFormat{Started: dirs})
	if err != nil {
		return err
	}
	return AtomicWriteFile(s.path, data, 0o644)
}

// AtomicWriteFile atomically writes data to path with the given mode by first writing
// to a temporary file in path's parent directory and then renaming it over path.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing to %s: %w", tmpPath, err)
	}
	// Sync before the rename, or the rename can be durable while the bytes it
	// points at are not: a crash then leaves a zero-length file where a valid
	// one used to be. The rename alone only ever bought atomicity against
	// concurrent readers, never crash safety, though this function's name and
	// the commit that introduced it both claimed otherwise.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, path, err)
	}
	// And sync the directory, so the rename itself survives a crash. Failure
	// here is not worth losing the write over — the data is already on disk and
	// the rename has happened, this only pins down when it becomes durable —
	// and some filesystems refuse the open outright.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// statePath returns the path to the state file, honoring $XDG_CONFIG_HOME
// and falling back to ~/.config — mirroring config.SettingsPath, duplicated
// rather than imported so this package stays a leaf with no dependency on
// internal/config.
func statePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wyrm", FileName), nil
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
