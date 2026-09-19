package sessions

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/tmux"
)

// TestListAndKillIntegration drives a real tmux server on an isolated socket:
// creates two sessions, verifies ListSessions reports them (most-recently
// active first), then kills one through KillSession and confirms it's gone.
// Skipped with -short or without tmux.
func TestListAndKillIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-sess-it-%d", os.Getpid())}
	t.Cleanup(func() { r.Run("kill-server") }) //nolint:errcheck

	// No server running yet: ListSessions must report empty, not error.
	if got, err := List(r); err != nil || len(got) != 0 {
		t.Fatalf("ListSessions before any server: got %v, err %v; want empty, nil", got, err)
	}

	for _, name := range []string{"alpha", "beta"} {
		if out, err := r.Run("new-session", "-d", "-s", name); err != nil {
			t.Fatalf("new-session %q: %v (%s)", name, err, out)
		}
	}

	got, err := List(r)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2: %v", len(got), names(got))
	}
	seen := map[string]Session{}
	for _, s := range got {
		seen[s.Name] = s
	}
	if _, ok := seen["alpha"]; !ok {
		t.Errorf("alpha missing from %v", names(got))
	}
	if s, ok := seen["beta"]; !ok || s.Windows != 1 {
		t.Errorf("beta wrong: %+v", s)
	}

	if err := Kill(r, seen["alpha"].ID); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	after, err := List(r)
	if err != nil {
		t.Fatalf("ListSessions after kill: %v", err)
	}
	if len(after) != 1 || after[0].Name != "beta" {
		t.Fatalf("after kill got %v, want just beta", names(after))
	}
}

// TestDottedSessionNameIntegration guards against the bug where a session
// named like "wyrm.vim" couldn't be targeted by name: tmux's -t target
// syntax treats "." as the window.pane separator and misparses such names,
// even with an "=" exact-match prefix. ListSessions + KillSession must work
// end-to-end against a real tmux server for a session named this way, since
// they operate on the session's tmux ID rather than its name.
func TestDottedSessionNameIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-sess-dot-it-%d", os.Getpid())}
	t.Cleanup(func() { r.Run("kill-server") }) //nolint:errcheck

	if out, err := r.Run("new-session", "-d", "-s", "wyrm.vim"); err != nil {
		// Whether a "." in a session name is preserved, silently sanitized to
		// "_", or rejected outright at creation time varies across tmux
		// builds (observed all three across local, Ubuntu CI, and macOS CI
		// runners, even nominally the same tmux version) — when it's
		// rejected, this build never lets the ambiguous name exist in the
		// first place, so the bug this test guards against can't occur.
		if strings.Contains(out, "invalid session name") {
			t.Skip(`this tmux build rejects "." in session names outright; the bug this test guards against doesn't apply here`)
		}
		t.Fatalf("new-session: %v (%s)", err, out)
	}

	got, err := List(r)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 1 || got[0].ID == "" {
		t.Fatalf("ListSessions = %+v, want one session with a non-empty ID", got)
	}
	if got[0].Name != "wyrm.vim" {
		// Some tmux builds (observed with the apt-packaged tmux on Ubuntu CI
		// runners) silently sanitize "." to "_" in session names at creation
		// time, unlike newer builds which preserve them literally — that's
		// the precondition this test needs, so skip cleanly if it doesn't
		// hold here.
		t.Skipf(`this tmux build sanitizes "." in session names (got %q); the bug this test guards against doesn't apply here`, got[0].Name)
	}

	if err := Kill(r, got[0].ID); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	after, err := List(r)
	if err != nil {
		t.Fatalf("ListSessions after kill: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("after kill got %v, want none", names(after))
	}
}
