package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// TestIntegration drives the model's real command/message flow against a live
// tmux server on an isolated socket (no TTY needed — the reducer and the tmux
// command closures are exercised directly). It creates a session, echoes a
// sentinel into the active pane, then walks Sessions->Windows->Panes->preview
// and asserts the sentinel shows up in the captured preview. Skipped with
// -short or without tmux.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-tui-it-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	if out, err := r.Run("new-session", "-d", "-s", "ittui", "-n", "code"); err != nil {
		t.Fatalf("new-session: %v (%s)", err, out)
	}
	if out, err := r.Run("split-window", "-t", "ittui", "-h"); err != nil {
		t.Fatalf("split-window: %v (%s)", err, out)
	}
	if out, err := r.Run("send-keys", "-t", "ittui", "echo WYRMSENTINEL", "Enter"); err != nil {
		t.Fatalf("send-keys: %v (%s)", err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := r.Run("capture-pane", "-p", "-t", "ittui")
		if err == nil && strings.Contains(out, "WYRMSENTINEL") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sentinel did not reach the tmux pane: %v (%s)", err, out)
		}
		time.Sleep(20 * time.Millisecond)
	}

	m := New(r, nil)
	m.width, m.height, m.ready = 120, 40, true

	// Drive the load chain the way the event loop would: execute each returned
	// command and feed its message back into Update.
	msg := run(loadSessions(r))
	for i := 0; i < 8 && msg != nil; i++ {
		var cmd tea.Cmd
		m, cmd = update(m, msg)
		if m.preview != "" && strings.Contains(m.preview, "WYRMSENTINEL") {
			break
		}
		msg = run(cmd)
	}

	if len(m.sessions) == 0 {
		t.Fatal("no sessions loaded from live tmux")
	}
	if len(m.windows) == 0 {
		t.Fatal("no windows loaded for the selected session")
	}
	if len(m.panes) == 0 {
		t.Fatal("no panes loaded for the selected window")
	}
	if !strings.Contains(m.preview, "WYRMSENTINEL") {
		t.Errorf("preview did not capture the sentinel; got:\n%s", m.preview)
	}

	// The rendered frame should stay within the terminal height.
	if lines := strings.Count(m.View(), "\n") + 1; lines > m.height {
		t.Errorf("View() rendered %d lines, exceeds height %d", lines, m.height)
	}
}

// TestIntegrationProject exercises the Phase 3 project lifecycle: start a
// session from a config, confirm it's running, then stop it. Skipped with
// -short or without tmux.
func TestIntegrationProject(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-tui-proj-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	dir := t.TempDir()
	path := filepath.Join(dir, ".wyrm.toml")
	cfg := "[session]\nname = \"itproj\"\nroot = \"" + dir + "\"\n\n[[windows]]\nname = \"main\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	started, ok := run(startProjectCmd(r, nil, Project{Path: path})).(projectStartedMsg)
	if !ok || started.err != nil {
		t.Fatalf("startProjectCmd -> %+v", started)
	}
	if started.sessionID == "" {
		t.Fatal("startProjectCmd returned an empty session ID")
	}
	running, _ := sessions.List(r)
	if !hasSession(running, "itproj") {
		t.Fatalf("session 'itproj' not running after start; have %+v", running)
	}

	msg, ok := run(killProjectCmd(r, nil, config.Project{Path: path})).(projectsMsg)
	if !ok || msg.err != nil {
		t.Fatalf("killProjectCmd -> %+v", msg)
	}
	running, _ = sessions.List(r)
	if hasSession(running, "itproj") {
		t.Errorf("session 'itproj' still running after stop; have %+v", running)
	}
}

func hasSession(list []sessions.Session, name string) bool {
	for _, s := range list {
		if s.Name == name {
			return true
		}
	}
	return false
}

// TestIntegrationManagement exercises the Phase 2 mutation commands (new-window,
// rename-window, kill-window) against a real tmux server and asserts the
// resulting window list each step. Skipped with -short or without tmux.
func TestIntegrationManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-tui-mgmt-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	if out, err := r.Run("new-session", "-d", "-s", "mgmt", "-n", "first"); err != nil {
		t.Fatalf("new-session: %v (%s)", err, out)
	}
	sessions, err := sessions.List(r)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("ListSessions: %v (%d sessions)", err, len(sessions))
	}
	sid := sessions[0].ID

	// new-window -> 2 windows.
	msg := run(newWindowCmd(r, sid, "second"))
	wm, ok := msg.(windowsMsg)
	if !ok || wm.err != nil {
		t.Fatalf("newWindowCmd -> %T (err=%v)", msg, wm.err)
	}
	if len(wm.windows) != 2 {
		t.Fatalf("after new-window: %d windows, want 2", len(wm.windows))
	}

	// rename the new window and confirm it took.
	var newWinID string
	for _, w := range wm.windows {
		if w.Name == "second" {
			newWinID = w.ID
		}
	}
	if newWinID == "" {
		t.Fatal("could not find the newly created 'second' window")
	}
	msg = run(renameWindowCmd(r, sid, newWinID, "renamed"))
	wm, _ = msg.(windowsMsg)
	renamed := false
	for _, w := range wm.windows {
		if w.ID == newWinID && w.Name == "renamed" {
			renamed = true
		}
	}
	if !renamed {
		t.Errorf("rename did not take; windows=%+v", wm.windows)
	}

	// kill it -> back to 1 window.
	msg = run(killWindowCmd(r, sid, newWinID))
	wm, ok = msg.(windowsMsg)
	if !ok || wm.err != nil {
		t.Fatalf("killWindowCmd -> %T (err=%v)", msg, wm.err)
	}
	if len(wm.windows) != 1 {
		t.Errorf("after kill-window: %d windows, want 1", len(wm.windows))
	}
}
