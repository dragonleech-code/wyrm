package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// git reports the toplevel with symlinks resolved, and macOS's TempDir is
	// under /var -> /private/var, so compare against what git will say.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@example.test", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestWorktreeCreatesBranchAndStartsSession(t *testing.T) {
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".wyrm.toml"), []byte("[session]\nname = \"app\"\nroot = \".\"\n[[windows]]\nname = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, repo)

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	code := run([]string{"worktree", "-d", "feature/login"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	want := filepath.Join(filepath.Dir(repo), "app.worktrees", "feature-login")
	if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
		t.Fatalf("no worktree at %s: %v", want, err)
	}
	// session.name is cleared, so the session is named for the worktree, not
	// "app" — every worktree of a repo would otherwise collide.
	if !strings.Contains(stdout.String(), "created session app-feature-login") {
		t.Errorf("stdout = %q", stdout.String())
	}
	var newSession []string
	for _, c := range r.calls {
		if c[0] == "new-session" {
			newSession = c
		}
	}
	if !strings.Contains(strings.Join(newSession, " "), "-c "+want) {
		t.Errorf("session not rooted in the worktree: %v", newSession)
	}

	// Running it again reuses the worktree rather than failing.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"worktree", "-d", "feature/login"}, &stdout, &stderr, &fakeRunner{}, func() bool { return false }, nil); code != 0 {
		t.Fatalf("second run: exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists; using it") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestWorktreeOutsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	chdir(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"worktree", "x"}, &stdout, &stderr, &fakeRunner{}, nil, nil); code != 1 || !strings.Contains(stderr.String(), "not inside a git repository") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestWorktreeDirName(t *testing.T) {
	if got := worktreeDirName("feature/a b"); got != "feature-a-b" {
		t.Errorf("got %q", got)
	}
}
