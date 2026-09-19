package state

import (
	"os"
	"testing"
)

func TestMarkStartedLockTimeoutDoesNotWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := s.MarkStarted("/blocked"); err == nil {
		t.Fatal("expected lock timeout")
	}
	if s.Started("/blocked") {
		t.Fatal("failed write committed in memory")
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatalf("wrote without lock: %v", err)
	}
}

func TestMarkStartedPreservesCorruptedHistory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(s.path, []byte("not valid ["), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStarted("/new"); err == nil {
		t.Fatal("expected read failure")
	}
	data, err := os.ReadFile(s.path)
	if err != nil || string(data) != "not valid [" {
		t.Fatalf("overwrote corrupt history: %q %v", data, err)
	}
}
