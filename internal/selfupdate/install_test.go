package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRenameFailurePreservesOldBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wyrm")
	if err := os.WriteFile(path, []byte("old binary"), 0o744); err != nil {
		t.Fatal(err)
	}
	if err := installWithOps(path, []byte("new binary"), 0o744,
		func(string, string) error { return errors.New("rename failed") }, syncDirectory); err == nil {
		t.Fatal("expected rename failure")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old binary" {
		t.Fatalf("old binary changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary update file left behind: %v, %v", entries, err)
	}
}

func TestInstallDirectorySyncFailureReportsInstalledBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wyrm")
	err := installWithOps(path, []byte("new binary"), 0o755, os.Rename,
		func(string) error { return errors.New("sync failed") })
	if err == nil || !strings.Contains(err.Error(), "binary installed") {
		t.Fatalf("unexpected error: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "new binary" {
		t.Fatalf("replacement missing after sync failure: %q, %v", data, readErr)
	}
}

func TestInstallReplacesFilePreservingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wyrm")
	if err := os.WriteFile(path, []byte("old binary"), 0o744); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := Install(path, []byte("new binary"), info.Mode()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new binary" {
		t.Errorf("content = %q, want %q", data, "new binary")
	}
	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if newInfo.Mode() != info.Mode() {
		t.Errorf("mode = %v, want %v", newInfo.Mode(), info.Mode())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries after Install, want 1 (no leftover temp file): %v", len(entries), entries)
	}
}

func TestInstallCreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wyrm")
	if err := Install(path, []byte("fresh"), 0o755); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh" {
		t.Errorf("content = %q, want %q", data, "fresh")
	}
}
