package trust

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jskoll/wyrm/internal/config"
)

func TestAllowDenyForgetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wyrm", FileName)
	s, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Check("/p/.wyrm.toml", "abc"); got != Unknown {
		t.Fatalf("fresh store: %v", got)
	}
	if err := s.Allow("/p/.wyrm.toml", "abc"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Check("/p/.wyrm.toml", "abc"); got != Trusted {
		t.Errorf("after allow: %v", got)
	}
	if got := reloaded.Check("/p/.wyrm.toml", "def"); got != Changed {
		t.Errorf("edited config: %v", got)
	}
	if err := reloaded.Deny("/p/.wyrm.toml"); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Check("/p/.wyrm.toml", "abc"); got != Denied {
		t.Errorf("after deny: %v", got)
	}
	if err := reloaded.Forget("/p/.wyrm.toml"); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Check("/p/.wyrm.toml", "abc"); got != Unknown {
		t.Errorf("after forget: %v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestUpdateMergesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	a, _ := LoadFrom(path)
	b, _ := LoadFrom(path)
	if err := a.Allow("/a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Allow("/b", "2"); err != nil {
		t.Fatal(err)
	}
	c, _ := LoadFrom(path)
	if c.Check("/a", "1") != Trusted || c.Check("/b", "2") != Trusted {
		t.Errorf("an earlier writer's decision was lost: %+v", c.Entries())
	}
}

func loadCfg(t *testing.T, dir, body string) *config.Config {
	t.Helper()
	path := filepath.Join(dir, ".wyrm.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRequired(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv(EnvTrustAll, "")
	withHook := "[session]\nname = \"x\"\non_project_start = \"make\"\n[[windows]]\nname = \"w\"\n"
	repo := loadCfg(t, t.TempDir(), withHook)
	settings := &config.Settings{}

	if !Required(settings, repo) {
		t.Error("repo config with a hook should need trust")
	}
	if Required(settings, loadCfg(t, t.TempDir(), "[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\n")) {
		t.Error("config with no commands should not need trust")
	}
	own := filepath.Join(xdg, "wyrm", "settings")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if Required(settings, loadCfg(t, own, withHook)) {
		t.Error("config in the user's shared dir should not need trust")
	}
	off := false
	if Required(&config.Settings{Trust: config.TrustSettings{Enabled: &off}}, repo) {
		t.Error("trust disabled in settings")
	}
	t.Setenv(EnvTrustAll, "1")
	if Required(settings, repo) {
		t.Error("WYRM_TRUST_ALL=1 should skip trust")
	}
}
