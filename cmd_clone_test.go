package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/config"
)

// installFakeGit puts a fake "git" script at the front of PATH, standing in
// for the real thing so these tests never touch the network. It only
// implements enough of `git clone <repo> [dest]` to be useful here: creating
// the destination directory, the way a real clone would, so the rest of the
// clone command (chdir into it, resolve a config, build a session) has
// something real to operate on.
func installFakeGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git script requires a POSIX shell")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" != \"clone\" ]; then exit 1; fi\n" +
		"shift\n" +
		"if [ \"$1\" = \"--\" ]; then shift; fi\n" +
		"repo=\"$1\"\n" +
		"dest=\"$2\"\n" +
		"if [ -z \"$dest\" ]; then dest=$(basename \"$repo\" .git); fi\n" +
		"mkdir -p \"$dest\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCloneRequiresGit guards the explicit, named-subcommand-only dependency:
// wyrm clone needs git on PATH, but nothing else in wyrm does.
func TestCloneRequiresGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // a PATH with nothing on it, git included

	var stdout, stderr bytes.Buffer
	code := run([]string{"clone", "https://example.com/repo.git"}, &stdout, &stderr, &fakeRunner{},
		func() bool { return false }, nil)
	if code == 0 {
		t.Fatal("clone without git on PATH: want a non-zero exit")
	}
	if !strings.Contains(stderr.String(), "requires git") {
		t.Errorf("stderr = %q, want a message naming git as the requirement", stderr.String())
	}
}

// TestCloneBuildsAndAttachesSession covers the common path: clone into a
// derived destination (no explicit dest given), then build a session there
// exactly as a bare `wyrm up` run from inside it would — using the built-in
// default config, since the freshly "cloned" directory has none of its own.
func TestCloneBuildsAndAttachesSession(t *testing.T) {
	installFakeGit(t)
	origin := t.TempDir()
	chdir(t, origin)

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	attachCalled := false
	attach := func(string) error { attachCalled = true; return nil }

	code := run([]string{"clone", "https://example.com/myrepo.git"}, &stdout, &stderr, r,
		func() bool { return false }, attach)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	// clone chdirs into the destination before building the session (so a
	// relative session.root in a committed config resolves against it), so
	// check by absolute path rather than one relative to the process's
	// now-changed working directory.
	if _, err := os.Stat(filepath.Join(origin, "myrepo")); err != nil {
		t.Fatalf("clone did not create the derived destination directory: %v", err)
	}
	if !strings.Contains(stdout.String(), "created session") {
		t.Errorf("stdout = %q, want a session to be built for the cloned repo", stdout.String())
	}
	if !attachCalled {
		t.Error("clone did not attach after building the session")
	}
}

// TestCloneExplicitDestination covers the positional [dest] argument, mirroring
// `git clone <repo> <dest>`'s own signature.
func TestCloneExplicitDestination(t *testing.T) {
	installFakeGit(t)
	origin := t.TempDir()
	chdir(t, origin)

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := run([]string{"clone", "https://example.com/myrepo.git", "somewhere-else"}, &stdout, &stderr, r,
		func() bool { return false }, func(string) error { return nil })
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(origin, "somewhere-else")); err != nil {
		t.Fatalf("clone did not create the explicit destination: %v", err)
	}
}

// TestCloneUsesLocalConfigIfPresent: a repo that commits its own .wyrm.toml
// should build from that, the same as any other project — clone is a
// shortcut to "get the repo, then run wyrm here", not a separate build path.
func TestCloneUsesLocalConfigIfPresent(t *testing.T) {
	installFakeGit(t)
	base := t.TempDir()
	chdir(t, base)

	// The fake git script only mkdir's the destination; drop the config into
	// it ourselves, standing in for one that was already committed to the repo.
	dest := filepath.Join(base, "committed")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "[session]\nname = \"fromrepo\"\nroot = \".\"\n\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(dest, config.DefaultFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := run([]string{"clone", "https://example.com/x.git", "committed"}, &stdout, &stderr, r,
		func() bool { return false }, func(string) error { return nil })
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created session fromrepo") {
		t.Errorf("stdout = %q, want the repo's own committed config to be used", stdout.String())
	}
}

func TestCloneNoStart(t *testing.T) {
	installFakeGit(t)
	origin := t.TempDir()
	chdir(t, origin)

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := run([]string{"clone", "-no-start", "https://example.com/nostart.git"}, &stdout, &stderr, r,
		func() bool { return false }, func(string) error { return nil })
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cloned https://example.com/nostart.git to") {
		t.Errorf("stdout = %q, want clone confirmation without starting session", stdout.String())
	}
	if strings.Contains(stdout.String(), "created session") {
		t.Errorf("stdout = %q, did not want session to be created with -no-start", stdout.String())
	}
}

// A freshly cloned repository is the one input the user has not read yet, so
// clone lists the shell its config would run and asks before running it. The
// old behavior printed a warning naming the flag you needed *before* you ran
// the command, and then ran the hooks anyway.
func TestCloneConfirmsBeforeRunningHooks(t *testing.T) {
	writeHookedClone := func(t *testing.T) string {
		t.Helper()
		base := t.TempDir()
		chdir(t, base)
		dest := filepath.Join(base, "withhooks")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "[session]\nname = \"hooked\"\nroot = \".\"\n" +
			"on_project_start = \"echo pwned\"\n\n[[windows]]\nname = \"w\"\n" +
			"  [[windows.splits]]\n  command = \"also-this\"\n"
		if err := os.WriteFile(filepath.Join(dest, config.DefaultFileName), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dest
	}

	t.Run("declining does not build the session", func(t *testing.T) {
		installFakeGit(t)
		writeHookedClone(t)
		opts := withStdin("n\n")

		r := &fakeRunner{}
		var stdout, stderr bytes.Buffer
		code := runWith(opts, []string{"clone", "https://example.com/x.git", "withhooks"}, &stdout, &stderr, r,
			func() bool { return false }, func(string) error { return nil })
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		// Every executable line is listed, not just on_project_start.
		for _, want := range []string{"on_project_start = echo pwned", "command = also-this"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want it to list %q", stderr.String(), want)
			}
		}
		if strings.Contains(stdout.String(), "created session") {
			t.Errorf("stdout = %q, declining must not build the session", stdout.String())
		}
		for _, c := range r.calls {
			if len(c) > 0 && c[0] == "new-session" {
				t.Errorf("declining still ran %v", c)
			}
		}
	})

	t.Run("accepting builds the session", func(t *testing.T) {
		installFakeGit(t)
		writeHookedClone(t)
		opts := withStdin("y\n")

		r := &fakeRunner{}
		var stdout, stderr bytes.Buffer
		code := runWith(opts, []string{"clone", "https://example.com/x.git", "withhooks"}, &stdout, &stderr, r,
			func() bool { return false }, func(string) error { return nil })
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "created session") {
			t.Errorf("stdout = %q, want the session to be built after confirming", stdout.String())
		}
	})

	t.Run("non-interactive declines rather than running unattended", func(t *testing.T) {
		installFakeGit(t)
		writeHookedClone(t)
		opts := withStdin("")

		r := &fakeRunner{}
		var stdout, stderr bytes.Buffer
		code := runWith(opts, []string{"clone", "https://example.com/x.git", "withhooks"}, &stdout, &stderr, r,
			func() bool { return false }, func(string) error { return nil })
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if strings.Contains(stdout.String(), "created session") {
			t.Errorf("stdout = %q, an unreadable stdin must decline", stdout.String())
		}
	})

	t.Run("-y skips the prompt", func(t *testing.T) {
		installFakeGit(t)
		writeHookedClone(t)
		opts := withStdin("")

		r := &fakeRunner{}
		var stdout, stderr bytes.Buffer
		code := runWith(opts, []string{"clone", "-y", "https://example.com/x.git", "withhooks"}, &stdout, &stderr, r,
			func() bool { return false }, func(string) error { return nil })
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "created session") {
			t.Errorf("stdout = %q, want -y to build without prompting", stdout.String())
		}
	})
}

// withStdin returns run options whose prompt reader is fixed input, for a test
// that has to answer a confirmation prompt.
func withStdin(in string) runOptions {
	return runOptions{stdin: strings.NewReader(in)}
}

func TestCloneWrongArgCount(t *testing.T) {
	installFakeGit(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"clone"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code == 0 {
		t.Error("clone with no repository argument: want a non-zero exit")
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"clone", "a", "b", "c"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
	if code == 0 {
		t.Error("clone with too many arguments: want a non-zero exit")
	}
}

func TestDeriveCloneDir(t *testing.T) {
	tests := []struct {
		repo, want string
	}{
		{"https://github.com/jskoll/wyrm.git", "wyrm"},
		{"https://github.com/jskoll/wyrm", "wyrm"},
		{"https://github.com/jskoll/wyrm/", "wyrm"},
		{"git@github.com:jskoll/wyrm.git", "wyrm"},
		{"git@host:repo.git", "repo"},
	}
	for _, tt := range tests {
		if got := deriveCloneDir(tt.repo); got != tt.want {
			t.Errorf("deriveCloneDir(%q) = %q, want %q", tt.repo, got, tt.want)
		}
	}
}

func TestConfigCommandsIncludesExecutablePaneTitle(t *testing.T) {
	enabled := true
	cfg := &config.Config{}
	cfg.Session.EnablePaneTitles = &enabled
	cfg.Session.PaneTitleFormat = "#(touch /tmp/owned)"
	commands := configCommands(cfg)
	if len(commands) != 1 || !strings.Contains(commands[0], "pane_title_format") {
		t.Fatalf("commands = %v, want pane title command substitution", commands)
	}
}

func TestClonePaneTitleCommandRequiresConfirmation(t *testing.T) {
	installFakeGit(t)
	base := t.TempDir()
	chdir(t, base)
	dest := filepath.Join(base, "title-command")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "[session]\nname = \"title-command\"\nroot = \".\"\nenable_pane_titles = true\npane_title_format = \"#(touch /tmp/owned)\"\n[[windows]]\nname = \"w\"\n"
	if err := os.WriteFile(filepath.Join(dest, config.DefaultFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := runWith(withStdin("n\n"), []string{"clone", "https://example.com/x.git", "title-command"},
		&stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 || !strings.Contains(stderr.String(), "pane_title_format") {
		t.Fatalf("clone result = %d, stderr = %q", code, stderr.String())
	}
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "new-session" {
			t.Fatalf("declined pane-title command still started a session: %v", call)
		}
	}
}

func TestCloneRejectsFlags(t *testing.T) {
	installFakeGit(t)

	flagArgs := [][]string{
		{"clone", "--", "--upload-pack=touch /tmp/pwned", "repo"},
		{"clone", "--", "-u", "repo"},
		{"clone", "--", "--config=core.sshCommand=calc", "repo"},
		{"clone", "https://example.com/repo.git", "--dest-flag"},
		{"clone", "https://example.com/repo.git", "-d"},
		{"clone", "-u", "repo"},
	}

	for _, args := range flagArgs {
		var stdout, stderr bytes.Buffer
		code := run(args, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil)
		if code == 0 {
			t.Errorf("run(%v): want non-zero exit code for arguments starting with hyphen", args)
		}
	}
}
