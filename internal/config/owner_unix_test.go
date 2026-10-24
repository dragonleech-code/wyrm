//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverRefusesWorldWritableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFileName)
	write(t, path, "[session]\nname = \"x\"\n")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverIn(dir, false)
	if err == nil || !strings.Contains(err.Error(), "writable by every user") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverRefusesConfigInForeignWorldWritableDir(t *testing.T) {
	// /tmp is the case this exists for: world-writable, sticky, owned by root.
	// A config sitting directly in it is refused even when the walk reaches it
	// from below.
	info, err := os.Stat(os.TempDir())
	if err != nil || info.Mode().Perm()&0o002 == 0 {
		t.Skip("system temp dir is not world-writable here")
	}
	if os.Getuid() == 0 {
		t.Skip("root owns the temp dir")
	}
	// Directly in the system temp dir, deliberately: t.TempDir() makes a
	// private subdirectory, which is exactly what this test must not use.
	f, err := os.CreateTemp("", "wyrm-owner-*.toml") //nolint:usetesting // see above
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	_ = f.Close()
	err = checkDiscoverable(os.TempDir(), f.Name())
	if err == nil || !strings.Contains(err.Error(), "is writable by every user") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverAcceptsOwnConfig(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), "[session]\nname = \"x\"\n")
	if _, err := DiscoverIn(dir, false); err != nil {
		t.Fatal(err)
	}
}
