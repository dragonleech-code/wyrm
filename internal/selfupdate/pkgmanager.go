package selfupdate

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jskoll/wyrm/internal/process"
)

// managedCheck is one package manager's ownership probe: its display name,
// the upgrade command wyrm suggests, and how to ask it whether it owns a
// given path.
type managedCheck struct {
	name, hint, lookCmd string
	args                func(path string) []string
}

var managedChecks = []managedCheck{
	{"dpkg", "sudo apt update && sudo apt install --only-upgrade wyrm", "dpkg",
		func(p string) []string { return []string{"-S", p} }},
	{"rpm", "sudo dnf upgrade wyrm", "rpm",
		func(p string) []string { return []string{"-qf", p} }},
	{"pacman", "yay -Syu wyrm-bin  (or: paru -Syu wyrm-bin)", "pacman",
		func(p string) []string { return []string{"-Qo", p} }},
}

// Managed reports whether path appears to belong to a system package
// manager rather than a standalone binary, returning that manager's name
// and the command wyrm suggests running instead of selfupdate.
//
// Homebrew is detected from the install path itself — its Cellar layout is
// unambiguous. dpkg, rpm, and pacman are asked directly instead, since a
// package could install to any prefix and there's no path shape to grep for.
func Managed(path string) (manager, hint string, ok bool) {
	resolved := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		resolved = r
	}
	if strings.Contains(resolved, "/Cellar/") || strings.Contains(resolved, "/Homebrew/") || strings.Contains(resolved, "/linuxbrew/") {
		return "Homebrew", "brew upgrade jskoll/tap/wyrm", true
	}
	for _, c := range managedChecks {
		if _, err := exec.LookPath(c.lookCmd); err != nil {
			continue
		}
		// Bounded like every other helper process: a wedged package database
		// lock must not hang selfupdate forever.
		cmd, cancel := process.Command(c.lookCmd, c.args(resolved)...)
		err := cmd.Run()
		cancel()
		if err == nil {
			return c.name, c.hint, true
		}
	}
	return "", "", false
}
