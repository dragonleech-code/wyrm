package config

import (
	"strings"
	"testing"
)

func TestRewriteSessionStringSyntax(t *testing.T) {
	for _, input := range []string{
		"[session] # preserve\nroot='.'\n",
		"['session']\n'root'='.'\n",
		"session.root = '.'\n",
		"session = { root = '.', name = 'x' }\n",
		"session = {}\n",
		"[session]\nname='x'\n",
		"[session]",
		"[session]\nroot = '''\n.'''\n",
		"'session.root' = 'unchanged'\n[session]\nroot='.'\n",
	} {
		t.Run(input, func(t *testing.T) {
			out, err := RewriteSessionString([]byte(input), "root", "/tmp/project")
			if err != nil {
				t.Fatal(err)
			}
			out, err = RewriteSessionString(out, "project_dir", "/tmp/owner")
			if err != nil {
				t.Fatal(err)
			}
			cfg, _, err := Decode(out)
			if err != nil {
				t.Fatalf("%s: %v", out, err)
			}
			if cfg.Session.Root != "/tmp/project" || cfg.Session.ProjectDir != "/tmp/owner" {
				t.Fatalf("wrong rewrite: %s", out)
			}
			if strings.Contains(input, "# preserve") && !strings.Contains(string(out), "# preserve") {
				t.Fatal("comment lost")
			}
			if strings.Contains(input, "unchanged") && !strings.Contains(string(out), "'session.root' = 'unchanged'") {
				t.Fatal("literal dotted key changed")
			}
		})
	}
}

func TestRootNeedsAbsoluteExpandedRelative(t *testing.T) {
	t.Setenv("WYRM_RELATIVE", "backend")
	if !RootNeedsAbsolute("$WYRM_RELATIVE") {
		t.Fatal("relative expansion needs anchoring")
	}
}
