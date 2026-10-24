package config

import (
	"fmt"
	"path"
	"strings"
)

// ForRemote rewrites cfg's directories for a build on another machine whose
// home directory is home: a leading "~" becomes that home rather than this
// machine's, and the config stops resolving relative paths against the
// directory it was loaded from, which only exists here.
//
// session.root must therefore be absolute or "~"-relative. Window and split
// roots may still be relative; they resolve against session.root as usual.
// $VAR references are expanded from this machine's environment.
func (c *Config) ForRemote(home string) error {
	rebase := func(p string) string {
		if p == "~" {
			return home
		}
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			return path.Join(home, rest)
		}
		return p
	}
	c.Session.Root = rebase(c.Session.Root)
	if !strings.HasPrefix(c.Session.Root, "/") && !strings.HasPrefix(c.Session.Root, "$") {
		return fmt.Errorf("session.root %q is relative; a remote build needs an absolute or ~/ root", c.Session.Root)
	}
	var splits func([]Split)
	splits = func(ss []Split) {
		for i := range ss {
			ss[i].Root = rebase(ss[i].Root)
			splits(ss[i].Children)
		}
	}
	for i := range c.Windows {
		c.Windows[i].Root = rebase(c.Windows[i].Root)
		splits(c.Windows[i].Splits)
	}
	c.dir = ""
	return nil
}
