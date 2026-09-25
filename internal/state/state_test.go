package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMarkStartedRecoversStaleLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	lock := s.Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * lockStaleAfter)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStarted("/proj/a"); err != nil {
		t.Fatal(err)
	}
	if !s.Started("/proj/a") {
		t.Fatal("recovered lock did not persist start")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock still present: %v", err)
	}
}

func TestProjectLockRefreshesHistoryAfterAnotherStart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := first.LockProject("/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.MarkStarted("/proj/a"); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	secondUnlock, err := second.LockProject("/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	defer secondUnlock()
	if !second.Started("/proj/a") {
		t.Fatal("second process did not see completed first start")
	}
}

func TestLoadMissingFileReturnsEmptyStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Started("/some/project") {
		t.Error("Started on a fresh store = true, want false")
	}
}

func TestMarkStartedPersistsAcrossLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.MarkStarted("/proj/a"); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}

	s2, err := Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if !s2.Started("/proj/a") {
		t.Error("Started(/proj/a) after MarkStarted+reload = false, want true")
	}
	if s2.Started("/proj/b") {
		t.Error("Started(/proj/b) = true, want false (never marked)")
	}
}

func TestMarkStartedIsIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := s.MarkStarted("/proj/a"); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := s.MarkStarted("/proj/a"); err != nil {
		t.Fatalf("second MarkStarted: %v", err)
	}
	if !s.Started("/proj/a") {
		t.Error("Started after two MarkStarted calls = false, want true")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if s.Started("/proj/a") {
		t.Error("nil Store Started = true, want false")
	}
	if err := s.MarkStarted("/proj/a"); err != nil {
		t.Errorf("nil Store MarkStarted: %v, want nil", err)
	}
}

func TestStartedEmptyDirAlwaysFalse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Started("") {
		t.Error(`Started("") = true, want false`)
	}
	if err := s.MarkStarted(""); err != nil {
		t.Fatalf("MarkStarted(\"\"): %v", err)
	}
	if s.Started("") {
		t.Error(`Started("") after MarkStarted("") = true, want false`)
	}
}

func TestLoadParsesExistingFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir := filepath.Join(xdg, "wyrm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "started = [\"/proj/a\", \"/proj/b\"]\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !s.Started("/proj/a") || !s.Started("/proj/b") {
		t.Error("Load did not pick up the pre-existing started entries")
	}
}

func TestLoadParseError(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir := filepath.Join(xdg, "wyrm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("[not valid"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Error("Load with malformed state file: want error, got nil")
	}
}

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "atomic.txt")

	data := []byte("hello atomic world")
	if err := AtomicWriteFile(path, data, 0o644); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(read) != string(data) {
		t.Errorf("read %q, want %q", read, data)
	}

	// Overwrite atomically
	newData := []byte("updated content")
	if err := AtomicWriteFile(path, newData, 0o644); err != nil {
		t.Fatalf("second AtomicWriteFile: %v", err)
	}

	read2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("second ReadFile: %v", err)
	}
	if string(read2) != string(newData) {
		t.Errorf("second read %q, want %q", read2, newData)
	}
}
