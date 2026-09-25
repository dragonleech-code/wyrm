package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSharedConfig plants a shared config for a project rooted at root.
func writeSharedConfig(t *testing.T, path, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[session]\nroot = \"" + root + "\"\n\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two projects with the same folder name both mapped to "<base>.wyrm.toml",
// so the second silently read the first one's config — building a session in
// the wrong root, under the wrong name, with no warning.
func TestSharedConfigPathDisambiguatesOnCollision(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "shared")
	settings := &Settings{Storage: StorageShared, SharedDir: shared}

	work := filepath.Join(home, "work", "api")
	personal := filepath.Join(home, "personal", "api")
	for _, d := range []string{work, personal} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// First project takes the plain name.
	first, err := settings.SharedConfigPath(work)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(first), "api"+DefaultFileName; got != want {
		t.Fatalf("first project got %q, want the plain %q", got, want)
	}
	writeSharedConfig(t, first, work)

	// Second project must not be handed the first one's file.
	second, err := settings.SharedConfigPath(personal)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("both projects resolved to %s — the second would read the first's config", first)
	}
	if filepath.Dir(second) != shared {
		t.Errorf("disambiguated path left the shared dir: %s", second)
	}

	// And the first project still resolves to the file it already has, so
	// nothing on disk needs migrating.
	again, err := settings.SharedConfigPath(work)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("owner's path moved from %s to %s", first, again)
	}

	// Stable across calls: the same project always gets the same file.
	repeat, err := settings.SharedConfigPath(personal)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != second {
		t.Errorf("disambiguated path is not stable: %s then %s", second, repeat)
	}
}

func TestSharedConfigPathKeepsLegacySubdirectoryRoot(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "work", "api")
	root := filepath.Join(project, "server")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := &Settings{Storage: StorageShared, SharedDir: filepath.Join(home, "shared")}
	plain := filepath.Join(settings.SharedDir, "api"+DefaultFileName)
	writeSharedConfig(t, plain, root)
	got, err := settings.SharedConfigPath(project)
	if err != nil || got != plain {
		t.Fatalf("SharedConfigPath = %q, %v; want %q", got, err, plain)
	}
}

// The whole scheme rests on being able to tell who owns a file. A config that
// predates this — no root, or a relative one — must read as unknown, because
// every caller treats unknown as "assume it is ours" to stay compatible.
func TestSharedConfigOwner(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	abs := filepath.Join(dir, "abs.wyrm.toml")
	writeSharedConfig(t, abs, proj)
	if owner, ok := SharedConfigOwner(abs); !ok || owner != proj {
		t.Errorf("absolute root: got (%q, %v), want (%q, true)", owner, ok, proj)
	}

	rel := filepath.Join(dir, "rel.wyrm.toml")
	writeSharedConfig(t, rel, ".")
	if owner, ok := SharedConfigOwner(rel); ok {
		t.Errorf("a relative root resolves against the shared dir and identifies nothing, got %q", owner)
	}

	none := filepath.Join(dir, "none.wyrm.toml")
	if err := os.WriteFile(none, []byte("[session]\nname = \"x\"\n\n[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := SharedConfigOwner(none); ok {
		t.Error("a config with no root identifies no project")
	}

	if _, ok := SharedConfigOwner(filepath.Join(dir, "missing.wyrm.toml")); ok {
		t.Error("a missing file owns nothing")
	}
}

// DiscoverGlobal is the read side of the same bug: standing in the second
// project, it returned the first project's config.
func TestDiscoverGlobalDoesNotReturnAnotherProjectsConfig(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "shared")
	settings := &Settings{Storage: StorageShared, SharedDir: shared}

	work := filepath.Join(home, "work", "api")
	personal := filepath.Join(home, "personal", "api")
	for _, d := range []string{work, personal} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSharedConfig(t, filepath.Join(shared, "api"+DefaultFileName), work)

	t.Chdir(personal)
	got, err := DiscoverGlobal(settings)
	if err == nil && got == filepath.Join(shared, "api"+DefaultFileName) {
		t.Fatalf("standing in %s, DiscoverGlobal returned %s — which belongs to %s", personal, got, work)
	}

	// The owner still finds its own config.
	t.Chdir(work)
	got, err = DiscoverGlobal(settings)
	if err != nil {
		t.Fatalf("owner cannot find its own shared config: %v", err)
	}
	if got != filepath.Join(shared, "api"+DefaultFileName) {
		t.Errorf("owner got %q, want its own shared config", got)
	}
}

// SamePath is what keeps ownership checks honest across symlinks. os.Getwd
// returns a resolved path while a config's session.root is whatever the user
// typed, so on macOS (/var -> /private/var) or a symlinked $HOME a plain ==
// reports two spellings of one directory as two different projects.
func TestSamePathResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if !SamePath(realDir, link) {
		t.Errorf("SamePath(%q, %q) = false; they are the same directory", realDir, link)
	}
	if !SamePath(realDir, realDir) {
		t.Error("a path is not the same as itself")
	}

	other := filepath.Join(dir, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if SamePath(realDir, other) {
		t.Error("two genuinely different directories compared equal")
	}

	// Paths that do not exist cannot be resolved, and must still compare by
	// spelling rather than blowing up or collapsing to equal.
	a := filepath.Join(dir, "missing-a")
	b := filepath.Join(dir, "missing-b")
	if !SamePath(a, a) {
		t.Error("a nonexistent path is not the same as itself")
	}
	if SamePath(a, b) {
		t.Error("two different nonexistent paths compared equal")
	}
}
