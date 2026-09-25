package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// Install atomically replaces the file at path with data, preserving mode.
// It writes to a temp file in path's directory and renames over the
// original, so a crash never leaves a half-written binary — and so the
// replacement works even though path is the binary currently running: a
// POSIX rename repoints the directory entry without disturbing a process
// still executing the old inode.
func Install(path string, data []byte, mode os.FileMode) error {
	return installWithOps(path, data, mode, os.Rename, syncDirectory)
}

func installWithOps(path string, data []byte, mode os.FileMode, rename func(string, string) error, syncDir func(string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wyrm-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting permissions on %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("binary installed, but syncing its directory failed: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
