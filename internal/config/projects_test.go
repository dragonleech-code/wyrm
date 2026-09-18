package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sharedSettings(t *testing.T, dir string) *Settings {
	t.Helper()
	return &Settings{Storage: StorageShared, SharedDir: dir}
}

// TestDiscoverProjectsFindsLocalAndShared covers the rules `wyrm <name>`,
// `list-configs`, and the TUI's Projects panel all share.
func TestDiscoverProjectsFindsLocalAndShared(t *testing.T) {
	shared := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(shared, "api"+DefaultFileName),
		"[session]\nname = \"api\"\n[[windows]]\nname = \"w\"\n")
	// No explicit name: a shared config falls back to the filename, never to
	// its root (which would name every shared project after the shared folder).
	write(filepath.Join(shared, "web"+DefaultFileName),
		"[session]\nroot = \".\"\n[[windows]]\nname = \"w\"\n")

	local := t.TempDir()
	t.Chdir(local)
	write(filepath.Join(local, DefaultFileName),
		"[session]\nname = \"here\"\n[[windows]]\nname = \"w\"\n")

	got := DiscoverProjects(sharedSettings(t, shared))
	if len(got) != 3 {
		t.Fatalf("got %d projects (%+v), want 3", len(got), got)
	}
	// Local first, then shared sorted by path.
	if got[0].Name != "here" || got[0].Shared {
		t.Errorf("first project = %+v, want the local one", got[0])
	}
	if got[1].Name != "api" || !got[1].Shared {
		t.Errorf("second project = %+v, want shared api", got[1])
	}
	if got[2].Name != "web" || !got[2].Shared {
		t.Errorf("third project = %+v, want shared web named from its filename", got[2])
	}

	if p, ok := FindProject(sharedSettings(t, shared), "api"); !ok || p.Path == "" {
		t.Errorf("FindProject(api) = %+v, %v; want the shared config", p, ok)
	}
	if _, ok := FindProject(sharedSettings(t, shared), "nope"); ok {
		t.Error("FindProject found a project that does not exist")
	}
}

// TestProjectNameCacheInvalidatesOnEdit: the TUI re-runs discovery every few
// seconds, so the result is memoized by (size, mtime) rather than re-parsing
// every config each time. An edit still has to be picked up — that timer exists
// precisely so the list tracks reality.
func TestProjectNameCacheInvalidatesOnEdit(t *testing.T) {
	shared := t.TempDir()
	path := filepath.Join(shared, "proj"+DefaultFileName)
	settings := sharedSettings(t, shared)
	t.Chdir(t.TempDir())

	if err := os.WriteFile(path, []byte("[session]\nname = \"before\"\n[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverProjects(settings); len(got) != 1 || got[0].Name != "before" {
		t.Fatalf("got %+v, want one project named 'before'", got)
	}
	// Cached: a second call must agree.
	if got := DiscoverProjects(settings); len(got) != 1 || got[0].Name != "before" {
		t.Fatalf("cached call got %+v, want the same answer", got)
	}

	if err := os.WriteFile(path, []byte("[session]\nname = \"after\"\n[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Some filesystems have coarse mtime granularity; the size differs here too,
	// but nudge mtime so the test doesn't depend on which one caught it.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverProjects(settings); len(got) != 1 || got[0].Name != "after" {
		t.Fatalf("after editing got %+v, want the new name", got)
	}
}

// TestProjectNameSharedConfigWithAbsoluteRootMatchesBuiltSessionName is the
// regression test for a shared config's disambiguated filename becoming its
// display name even when session.root is absolute: Session.Resolve derives
// the actual tmux session name from the resolved root, not from the file. A
// shared "disambiguated-hash.wyrm.toml" rooted at an absolute directory used
// to display and be discoverable as "disambiguated-hash" while the session
// it actually built was named after the root — the two never agreeing
// anywhere.
func TestProjectNameSharedConfigWithAbsoluteRootMatchesBuiltSessionName(t *testing.T) {
	shared := t.TempDir()
	projectRoot := t.TempDir()

	path := filepath.Join(shared, "disambiguated-hash"+DefaultFileName)
	content := "[session]\nroot = \"" + projectRoot + "\"\n\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	want := filepath.Base(projectRoot)
	if got := ProjectName(path, true); got != want {
		t.Errorf("ProjectName = %q, want %q (the resolved root's basename, matching what Session.Resolve builds)", got, want)
	}
}

// TestProjectNameSharedConfigWithRelativeRootUsesFilename preserves the
// behavior the shared-config guard exists to protect: a relative (or empty)
// root resolves against the shared directory itself, so deriving a name from
// it would name every such project after the shared folder instead of its
// own file.
func TestProjectNameSharedConfigWithRelativeRootUsesFilename(t *testing.T) {
	shared := t.TempDir()
	path := filepath.Join(shared, "proj"+DefaultFileName)
	content := "[session]\nroot = \".\"\n\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ProjectName(path, true); got != "proj" {
		t.Errorf("ProjectName = %q, want the filename-derived \"proj\"", got)
	}
}

// TestDiscoverWildcardProjectsExpandsPattern covers the plain (single-level)
// glob form, and that each match gets its own Project rooted at the matched
// directory, all sharing the one template config.
func TestDiscoverWildcardProjectsExpandsPattern(t *testing.T) {
	base := t.TempDir()
	template := filepath.Join(t.TempDir(), "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A non-directory match must be excluded.
	if err := os.WriteFile(filepath.Join(base, "notadir"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	settings := &Settings{Wildcard: []Wildcard{{Pattern: filepath.Join(base, "*"), Config: template}}}
	got := DiscoverWildcardProjects(settings)
	if len(got) != 2 {
		t.Fatalf("got %d wildcard projects, want 2: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, p := range got {
		if !p.Wildcard {
			t.Errorf("project %+v: Wildcard = false, want true", p)
		}
		if p.Path != template {
			t.Errorf("project %+v: Path = %q, want the template %q", p, p.Path, template)
		}
		if p.Root == "" || !filepath.IsAbs(p.Root) {
			t.Errorf("project %+v: Root must be an absolute matched directory", p)
		}
		names[p.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("names = %+v, want alpha and beta", names)
	}
}

// TestDiscoverWildcardProjectsRecursive covers the "/**" suffix, which must
// match nested directories at any depth, but not the base itself.
func TestDiscoverWildcardProjectsRecursive(t *testing.T) {
	base := t.TempDir()
	template := filepath.Join(t.TempDir(), "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "foo", "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Hidden directories and dependency directories that should be pruned
	for _, ign := range []string{".git/objects", "node_modules/pkg", "vendor/sub", "foo/venv/bin"} {
		if err := os.MkdirAll(filepath.Join(base, ign), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	settings := &Settings{Wildcard: []Wildcard{{Pattern: base + "/**", Config: template}}}
	got := DiscoverWildcardProjects(settings)
	roots := map[string]bool{}
	for _, p := range got {
		roots[p.Root] = true
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %+v, want exactly foo and foo/bar", roots)
	}
	if !roots[filepath.Join(base, "foo")] || !roots[filepath.Join(base, "foo", "bar")] {
		t.Errorf("roots = %+v, want %q and %q", roots,
			filepath.Join(base, "foo"), filepath.Join(base, "foo", "bar"))
	}
	if roots[base] {
		t.Error("recursive wildcard matched the base directory itself")
	}
}

// TestDiscoverProjectsWildcardDedup guards the identity model: a wildcard
// project's key is (template path, matched directory), not the template path
// alone — DiscoverProjects' normal file-based dedup would otherwise collapse
// every directory sharing one template down to a single entry.
func TestDiscoverProjectsWildcardDedup(t *testing.T) {
	base := t.TempDir()
	template := filepath.Join(t.TempDir(), "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())

	settings := &Settings{Wildcard: []Wildcard{{Pattern: filepath.Join(base, "*"), Config: template}}}
	got := DiscoverProjects(settings)
	if len(got) != 3 {
		t.Fatalf("DiscoverProjects with a 3-directory wildcard = %d projects, want 3: %+v", len(got), got)
	}
}

// TestDiscoverProjectsWildcardYieldsToOwnConfig covers the other half of the
// identity model: a pattern like "~/code/*" also matches the directory the
// user is standing in, and that directory's own config must not be shadowed
// by — or listed alongside — the template's synthesized entry.
func TestDiscoverProjectsWildcardYieldsToOwnConfig(t *testing.T) {
	base := t.TempDir()
	template := filepath.Join(t.TempDir(), "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(base, "mine")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[session]\nname = \"mine\"\nroot = \".\"\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(own, DefaultFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(own)

	settings := &Settings{Wildcard: []Wildcard{{Pattern: filepath.Join(base, "*"), Config: template}}}
	got := DiscoverProjects(settings)

	var mine []Project
	for _, p := range got {
		if p.Name == "mine" {
			mine = append(mine, p)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("project %q appears %d times, want once: %+v", "mine", len(mine), got)
	}
	if mine[0].Wildcard {
		t.Errorf("project mine = %+v, want the local config to win over the template", mine[0])
	}
	// The sibling the user has no config for still comes from the template.
	var other bool
	for _, p := range got {
		if p.Name == "other" && p.Wildcard {
			other = true
		}
	}
	if !other {
		t.Errorf("sibling directory dropped: %+v", got)
	}
}

// TestDiscoverWildcardProjectsOwnConfigKeepsRoot covers a matched directory
// with its own config, discovered from elsewhere: its config wins over the
// template, but it is still a directory the pattern matched, so Root is set
// like its template-built siblings' — the TUI's zoxide tracking keys on it.
func TestDiscoverWildcardProjectsOwnConfigKeepsRoot(t *testing.T) {
	base := t.TempDir()
	template := filepath.Join(t.TempDir(), "tmpl.wyrm.toml")
	if err := os.WriteFile(template, []byte("[[windows]]\nname = \"w\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(base, "mine")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[session]\nname = \"mine\"\nroot = \".\"\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(own, DefaultFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	settings := &Settings{Wildcard: []Wildcard{{Pattern: filepath.Join(base, "*"), Config: template}}}
	got := DiscoverWildcardProjects(settings)
	if len(got) != 1 {
		t.Fatalf("got %d projects, want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.Wildcard || p.Path != filepath.Join(own, DefaultFileName) {
		t.Errorf("project = %+v, want its own config, not the template", p)
	}
	if p.Root != own {
		t.Errorf("Root = %q, want %q", p.Root, own)
	}
}

// TestFindProjectAliasResolvesAfterExactName covers both halves of the
// documented rule: an alias resolves a project, and an exact project name
// always wins over an alias collision.
func TestFindProjectAliasResolvesAfterExactName(t *testing.T) {
	local := t.TempDir()
	t.Chdir(local)
	body := "[session]\nname = \"dotfiles\"\naliases = [\"dot\", \"df\"]\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(local, DefaultFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	settings := &Settings{Storage: StorageLocal}
	if p, ok := FindProject(settings, "dot"); !ok || p.Name != "dotfiles" {
		t.Errorf("FindProject(dot) = %+v, %v; want the dotfiles project", p, ok)
	}
	if p, ok := FindProject(settings, "df"); !ok || p.Name != "dotfiles" {
		t.Errorf("FindProject(df) = %+v, %v; want the dotfiles project", p, ok)
	}
	if _, ok := FindProject(settings, "nonexistent-alias"); ok {
		t.Error("FindProject matched an alias that was never declared")
	}
}

// A shared config with a relative root resolves against the shared directory,
// not the project, so it builds a session in the wrong place. Warn, don't refuse.
func TestCheckSharedRoot(t *testing.T) {
	tests := []struct {
		name   string
		shared bool
		root   string
		want   bool
	}{
		{"relative root in a shared config", true, "..", true},
		{"dot root in a shared config", true, ".", true},
		{"absolute root is fine", true, "/srv/api", false},
		{"tilde root is fine", true, "~/api", false},
		{"var root is fine", true, "$PROJECTS/api", false},
		{"no root is fine", true, "", false},
		{"a local config is never affected", false, ".", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Session: Session{Root: tt.root}}
			msg, bad := CheckSharedRoot(Project{Path: "p", Shared: tt.shared}, cfg)
			if bad != tt.want {
				t.Errorf("CheckSharedRoot = %q, %v; want bad=%v", msg, bad, tt.want)
			}
			if bad && msg == "" {
				t.Error("a reported problem needs a message")
			}
		})
	}
	if _, bad := CheckSharedRoot(Project{Shared: true}, nil); bad {
		t.Error("a nil config must not report a problem")
	}
}
