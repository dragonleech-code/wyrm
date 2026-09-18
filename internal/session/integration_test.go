package session_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/freeze"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/tmux"
)

// TestIntegration drives a real tmux server on an isolated socket: creates a
// session with both layout formats, checks the resulting windows/panes and
// lifecycle hooks, then kills it. Skipped with -short or without tmux.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-it-%d", os.Getpid())}
	t.Cleanup(func() { r.Run("kill-server") }) //nolint:errcheck

	root := t.TempDir()
	cfg := &config.Config{
		Session: config.Session{
			Name:           "wyrm-it",
			Root:           root,
			OnProjectStart: "touch started",
			OnProjectExit:  "touch exited",
			StartupWindow:  "code",
		},
		Windows: []config.Window{
			{Name: "code", Splits: []config.Split{
				{Command: "# editor placeholder"},
				{Type: "h", Size: 30, Children: []config.Split{{Type: "v"}}},
			}},
			{Name: "misc", Layout: "even-horizontal", Panes: []config.Pane{
				{Command: "# a"}, {Command: "# b"},
			}},
		},
	}

	name, _, created, err := session.Create(r, cfg, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "wyrm-it" {
		t.Fatalf("name = %q, want wyrm-it", name)
	}
	if !created {
		t.Error("created = false, want true for a fresh session")
	}

	if _, err := os.Stat(filepath.Join(root, "started")); err != nil {
		t.Error("on_project_start hook did not run in the session root")
	}

	out, err := r.Run("list-windows", "-t", name, "-F", "#{window_name}|#{window_panes}|#{window_active}")
	if err != nil {
		t.Fatalf("list-windows: %v (%s)", err, out)
	}
	windows := map[string]string{}
	activeWindow := ""
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			t.Fatalf("unexpected list-windows line %q", line)
		}
		windows[parts[0]] = parts[1]
		if parts[2] == "1" {
			activeWindow = parts[0]
		}
	}
	if got := windows["code"]; got != "3" {
		t.Errorf("window code has %s panes, want 3 (initial + split + nested child)", got)
	}
	if got := windows["misc"]; got != "2" {
		t.Errorf("window misc has %s panes, want 2", got)
	}
	if activeWindow != "code" {
		t.Errorf("active window = %q, want startup_window code", activeWindow)
	}

	if _, _, created, err := session.Create(r, cfg, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Create (second call): %v", err)
	} else if created {
		t.Error("created = true on second Create, want false for an already-running session")
	}
	out, err = r.Run("list-windows", "-t", name, "-F", "#{window_name}")
	if err != nil {
		t.Fatalf("list-windows after reattach: %v (%s)", err, out)
	}
	if got := strings.Count(out, "\n") + 1; got != 2 {
		t.Errorf("window count after reattach = %d, want 2 (session was rebuilt instead of reattached)", got)
	}

	if _, err := session.Kill(r, cfg, os.Stderr); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "exited")); err != nil {
		t.Error("on_project_exit hook did not run in the session root")
	}
	if _, err := r.Run("has-session", "-t", name); err == nil {
		t.Error("session still exists after Kill")
	}
}

// TestIntegrationDottedSessionName guards against the bug where a session
// named like "wyrm.vim" broke has-session, new-window, kill-session, and
// friends: tmux's -t target syntax treats "." as the window.pane separator
// and misparses such names. Create and Kill must work end-to-end against a
// real tmux server for a session named this way, since they target it by
// tmux session ID rather than by the raw name (see tmux.FindSessionID).
func TestIntegrationDottedSessionName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-it-dot-%d", os.Getpid())}
	t.Cleanup(func() { r.Run("kill-server") }) //nolint:errcheck

	root := t.TempDir()
	cfg := &config.Config{
		Session: config.Session{Name: "wyrm.vim", Root: root},
		Windows: []config.Window{
			{Name: "first"},
			{Name: "second"},
		},
	}

	name, sessionID, created, err := session.Create(r, cfg, os.Stdout, os.Stderr)
	if err != nil {
		// A few tmux builds reject "." in a session name outright rather than
		// preserving or rewriting it. Such a build never lets the ambiguous
		// name exist, so there's nothing here to guard against.
		if strings.Contains(err.Error(), "invalid session name") {
			t.Skip(`this tmux build rejects "." in session names outright; the bug this test guards against doesn't apply here`)
		}
		t.Fatalf("Create: %v", err)
	}
	if sessionID == "" || !created {
		t.Fatalf("Create = %q, %q, %v; want a non-empty ID and created=true", name, sessionID, created)
	}

	// Create reports the name tmux *actually* assigned, which is either
	// "wyrm.vim" (builds that preserve the dot) or "wyrm_vim" (builds that
	// rewrite "." and ":" to "_"). Both are fine; what matters is that every
	// later lookup uses that name, so a second run reattaches instead of
	// trying to create a duplicate. This used to be skipped on the rewriting
	// builds, leaving the bug uncovered on exactly the platforms that had it.
	if name != "wyrm.vim" && name != "wyrm_vim" {
		t.Fatalf("Create name = %q, want wyrm.vim or wyrm_vim", name)
	}
	if _, ok, err := tmux.FindSessionID(r, name); err != nil {
		t.Fatalf("FindSessionID: %v", err)
	} else if !ok {
		t.Fatalf("the session Create reported as %q cannot be found by that name", name)
	}

	// Reattach (second Create) must find the running session rather than
	// erroring out or rebuilding it.
	if _, secondID, created, err := session.Create(r, cfg, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Create (second call): %v", err)
	} else if created {
		t.Error("created = true on second Create, want false for an already-running session")
	} else if secondID != sessionID {
		t.Errorf("second Create sessionID = %q, want %q (same session)", secondID, sessionID)
	}

	if _, err := session.Kill(r, cfg, os.Stderr); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, ok, err := tmux.FindSessionID(r, "wyrm.vim"); err != nil || ok {
		t.Errorf("session still exists after Kill: ok=%v err=%v", ok, err)
	}
}

func TestIntegrationFreezeWorkingDirectoryRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-it-freeze-%d", os.Getpid())}
	t.Cleanup(func() { r.Run("kill-server") }) //nolint:errcheck

	root := t.TempDir()
	frontendDir := filepath.Join(root, "frontend")
	backendDir := filepath.Join(root, "backend")
	docsDir := filepath.Join(root, "docs")
	for _, d := range []string{frontendDir, backendDir, docsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Session: config.Session{
			Name: "freeze-dirs",
			Root: root,
		},
		Windows: []config.Window{
			{
				Name: "web",
				Root: "frontend",
				Splits: []config.Split{
					{Command: "# web-1"},
					{Type: "h", Size: 50, Command: "# web-2"},
				},
			},
			{
				Name: "mixed",
				Splits: []config.Split{
					{Root: "backend", Command: "# api"},
					{Type: "h", Size: 50, Root: "docs", Command: "# docs"},
				},
			},
		},
	}

	_, sessionID, _, err := session.Create(r, cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	frozen, err := freeze.Config(r, sessionID, "freeze-dirs-rebuilt", root)
	if err != nil {
		t.Fatalf("freeze.Config: %v", err)
	}

	if len(frozen.Windows) != 2 {
		t.Fatalf("expected 2 windows in frozen config, got %d", len(frozen.Windows))
	}
	if frozen.Windows[0].Root != "frontend" {
		t.Errorf("window 0 root = %q, want 'frontend'", frozen.Windows[0].Root)
	}
	if len(frozen.Windows[1].Splits) < 2 {
		t.Fatalf("window 1 has %d splits, want at least 2", len(frozen.Windows[1].Splits))
	}
	if frozen.Windows[1].Splits[0].Root != "backend" {
		t.Errorf("window 1 split 0 root = %q, want 'backend'", frozen.Windows[1].Splits[0].Root)
	}
	if frozen.Windows[1].Splits[1].Root != "docs" {
		t.Errorf("window 1 split 1 root = %q, want 'docs'", frozen.Windows[1].Splits[1].Root)
	}

	// Kill and rebuild from frozen config
	if _, err := session.Kill(r, cfg, io.Discard); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	_, rebuiltID, _, err := session.Create(r, frozen, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Create rebuilt: %v", err)
	}

	windows, err := tmux.ListWindows(r, rebuiltID)
	if err != nil {
		t.Fatalf("ListWindows rebuilt: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("rebuilt session has %d windows, want 2", len(windows))
	}

	samePath := func(a, b string) bool {
		ea, erra := filepath.EvalSymlinks(a)
		eb, errb := filepath.EvalSymlinks(b)
		if erra == nil && errb == nil {
			return filepath.Clean(ea) == filepath.Clean(eb)
		}
		return filepath.Clean(a) == filepath.Clean(b)
	}

	panes0, err := tmux.ListPanes(r, windows[0].ID)
	if err != nil {
		t.Fatalf("ListPanes window 0: %v", err)
	}
	for i, p := range panes0 {
		if !samePath(p.Path, frontendDir) {
			t.Errorf("window 0 pane %d path = %q, want %q", i, p.Path, frontendDir)
		}
	}

	panes1, err := tmux.ListPanes(r, windows[1].ID)
	if err != nil {
		t.Fatalf("ListPanes window 1: %v", err)
	}
	if len(panes1) == 2 {
		if !samePath(panes1[0].Path, backendDir) {
			t.Errorf("window 1 pane 0 path = %q, want %q", panes1[0].Path, backendDir)
		}
		if !samePath(panes1[1].Path, docsDir) {
			t.Errorf("window 1 pane 1 path = %q, want %q", panes1[1].Path, docsDir)
		}
	}
}
