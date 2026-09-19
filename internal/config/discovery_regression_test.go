package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWildcardOwnConfigAndWorktreeIdentity(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "local")
	if err := os.Mkdir(local, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, DefaultFileName), []byte("[session]\nname='own'\naliases=['alias']\n[[windows]]\nname='own-window'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(base, "branch")
	setupWorktree(t, filepath.Join(t.TempDir(), "repo"), worktree, "branch")
	template := filepath.Join(t.TempDir(), "template.toml")
	if err := os.WriteFile(template, []byte("[session]\nroot='.'\n[[windows]]\nname='template-window'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	projects := DiscoverWildcardProjects(&Settings{Wildcard: []Wildcard{{Pattern: filepath.Join(base, "*"), Config: template}}})
	if len(projects) != 2 {
		t.Fatalf("projects: %+v", projects)
	}
	for _, p := range projects {
		cfg, err := p.LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		switch p.Name {
		case "own":
			if p.Wildcard || len(p.Aliases) != 1 || cfg.Windows[0].Name != "own-window" {
				t.Fatalf("local config ignored: %+v", p)
			}
		case "repo-branch":
			if !p.Wildcard || cfg.Session.Root != worktree {
				t.Fatalf("wrong worktree: %+v", p)
			}
		default:
			t.Fatalf("wrong discovered name: %s", p.Name)
		}
	}
}

func TestProjectIndexLiveIdentityIgnoresAliases(t *testing.T) {
	ix := ProjectIndex{projects: []Project{{Name: "one", Aliases: []string{"two"}}, {Name: "a.b"}}}
	if _, ok := ix.FindSession("two"); ok {
		t.Fatal("live name matched an unrelated alias")
	}
	if p, ok := ix.FindSession("a_b"); !ok || p.Name != "a.b" {
		t.Fatal("sanitized name did not match")
	}
}
