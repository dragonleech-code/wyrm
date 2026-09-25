package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/editor"
)

// projectModel returns a model focused on the Projects panel with one project.
func projectModel(r *funcRunner, path string) Model {
	m := New(r, nil)
	m.focus = panelProjects
	m.projects = []Project{{Name: "webapp", Path: path}}
	m.cur[panelProjects] = 0
	m.previewSrc = previewConfig
	return m
}

func TestProjectsMsgClampsCursor(t *testing.T) {
	m := New(nopRunner(), nil)
	m.cur[panelProjects] = 5
	m, _ = update(m, projectsMsg{projects: []Project{{Name: "a"}, {Name: "b"}}})
	if len(m.projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(m.projects))
	}
	if m.cur[panelProjects] != 1 {
		t.Errorf("projectCur = %d, want clamped to 1", m.cur[panelProjects])
	}
}

func TestFocusProjectsLoadsConfigPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".wyrm.toml")
	if err := os.WriteFile(path, []byte("[session]\nname = \"webapp\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(nopRunner(), nil)
	m.projects = []Project{{Name: "webapp", Path: path}}
	m.focus = panelSessions // start off Projects

	// Switching focus to Projects (key "1") should request a config preview.
	m, cmd := update(m, key("1"))
	if m.previewSrc != previewConfig {
		t.Fatalf("previewSrc = %d, want previewConfig", m.previewSrc)
	}
	msg := run(cmd)
	cp, ok := msg.(configPreviewMsg)
	if !ok {
		t.Fatalf("focus->Projects produced %T, want configPreviewMsg", msg)
	}
	m, _ = update(m, cp)
	if !strings.Contains(m.preview, "webapp") {
		t.Errorf("config preview = %q, want it to contain the file contents", m.preview)
	}
}

func TestTickSkipsConfigPreview(t *testing.T) {
	m := New(nopRunner(), nil)
	m.focus = panelProjects
	m.previewSrc = previewConfig
	m.preview = "config contents"
	// A tick must not clobber a static config preview with a pane capture.
	m, cmd := update(m, tickMsg{})
	if m.preview != "config contents" {
		t.Errorf("tick overwrote the config preview: %q", m.preview)
	}
	// The ticker must still reschedule itself.
	if run(cmd) == nil {
		t.Error("tick should still reschedule the next tick")
	}
}

func TestStartProjectAttaches(t *testing.T) {
	// Point at a real, minimal config; the funcRunner fakes tmux so no server
	// is needed. session.Create issues new-session and returns its ID.
	dir := t.TempDir()
	path := filepath.Join(dir, ".wyrm.toml")
	cfg := "[session]\nname = \"proj\"\nroot = \"" + dir + "\"\n\n[[windows]]\nname = \"main\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &funcRunner{fn: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "", nil // nothing running yet
		case "new-session":
			return "$9|@1|%1", nil
		case "display-message":
			return "proj", nil
		}
		return "", nil
	}}
	m := projectModel(r, path)

	m, cmd := update(m, key("enter"))
	msg := run(cmd)
	ps, ok := msg.(projectStartedMsg)
	if !ok {
		t.Fatalf("enter on project produced %T, want projectStartedMsg", msg)
	}
	m, quit := update(m, ps)
	if m.pendingAttach != "$9" {
		t.Errorf("pendingAttach = %q, want $9", m.pendingAttach)
	}
	if _, ok := run(quit).(tea.QuitMsg); !ok {
		t.Error("starting a project should quit to hand off the attach")
	}
}

// TestStartProjectSurfacesHookWarningWithoutBlockingAttach is the regression
// test for startProjectCmd passing io.Discard to session.Create and
// RunAttachHook: a failed hook (a warning, not a build failure — see
// session.Create's error policy) used to vanish entirely, the TUI being the
// only place a project could start with something silently having gone
// wrong. The warning must be visible without blocking the attach the message
// already commits to.
func TestStartProjectSurfacesHookWarningWithoutBlockingAttach(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".wyrm.toml")
	cfg := "[session]\nname = \"proj\"\nroot = \"" + dir + "\"\non_project_start = \"exit 1\"\n\n[[windows]]\nname = \"main\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &funcRunner{fn: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "", nil
		case "new-session":
			return "$9|@1|%1", nil
		case "display-message":
			return "proj", nil
		}
		return "", nil
	}}
	m := projectModel(r, path)

	m, cmd := update(m, key("enter"))
	msg := run(cmd)
	ps, ok := msg.(projectStartedMsg)
	if !ok {
		t.Fatalf("enter on project produced %T, want projectStartedMsg", msg)
	}
	if ps.err != nil {
		t.Fatalf("projectStartedMsg.err = %v, want nil (a hook failure is a warning, not a build failure)", ps.err)
	}
	if ps.warnings == "" {
		t.Fatal("projectStartedMsg.warnings is empty, want the on_project_start failure captured")
	}

	m, quit := update(m, ps)
	if m.pendingAttach != "$9" {
		t.Errorf("pendingAttach = %q, want $9 — a warning must not block the attach", m.pendingAttach)
	}
	if m.err == nil {
		t.Error("m.err is nil, want the warning surfaced for runProgram to report after quitting")
	}
	if _, ok := run(quit).(tea.QuitMsg); !ok {
		t.Error("a warning must not prevent quitting into the attach")
	}
}

// TestKillProjectSurfacesHookWarning is the regression test for
// killProjectCmd passing io.Discard to session.Kill: a failed
// on_project_exit (a warning, not a kill failure) used to vanish entirely
// instead of being visible the way it is from the CLI.
func TestKillProjectSurfacesHookWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".wyrm.toml")
	cfg := "[session]\nname = \"proj\"\nroot = \"" + dir + "\"\non_project_exit = \"exit 1\"\n\n[[windows]]\nname = \"main\"\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &funcRunner{fn: func(args ...string) (string, error) {
		if args[0] == "list-sessions" {
			if len(args) > 2 && strings.Contains(args[2], "session_windows") {
				return "$1|1|0|100|proj", nil
			}
			return "$1|proj", nil
		}
		return "", nil
	}}

	msg := run(killProjectCmd(r, nil, config.Project{Path: path}))
	pm, ok := msg.(projectsMsg)
	if !ok {
		t.Fatalf("killProjectCmd produced %T, want projectsMsg", msg)
	}
	if pm.err != nil {
		t.Fatalf("projectsMsg.err = %v, want nil (a hook failure is a warning, not a kill failure)", pm.err)
	}
	if pm.warnings == "" {
		t.Fatal("projectsMsg.warnings is empty, want the on_project_exit failure captured")
	}
}

func TestEditProjectLaunchesEditor(t *testing.T) {
	t.Setenv("EDITOR", "nvim")
	m := projectModel(&funcRunner{fn: func(_ ...string) (string, error) { return "", nil }}, "/tmp/x/.wyrm.toml")

	// tea.ExecProcess wraps the command in an unexported message, so assert on
	// the command itself rather than round-tripping through the model.
	cmd, err := editor.Command(m.projects[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nvim", "/tmp/x/.wyrm.toml"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("editor command = %q, want %q", cmd.Args, want)
	}
	if _, c := update(m, key("e")); c == nil {
		t.Error("e on the Projects panel should produce a command")
	}
}

func TestEditProjectSurfacesResolutionError(t *testing.T) {
	t.Setenv("EDITOR", "   ")
	m := projectModel(&funcRunner{fn: func(_ ...string) (string, error) { return "", nil }}, "/tmp/x/.wyrm.toml")

	_, cmd := update(m, key("e"))
	if _, ok := run(cmd).(actionErrMsg); !ok {
		t.Error("an unusable $EDITOR should surface as an actionErrMsg, not a launch")
	}
}

func TestKillProjectRequiresRunning(t *testing.T) {
	r := &funcRunner{fn: func(_ ...string) (string, error) { return "", nil }}
	m := projectModel(r, "/tmp/x/.wyrm.toml")
	m.projects[0].Running = false
	// x on a not-running project is a no-op (no confirm modal).
	m, _ = update(m, key("x"))
	if m.mode != modeNormal {
		t.Errorf("x on a stopped project should not open a modal")
	}
	// A running project opens the stop-confirm modal.
	m.projects[0].Running = true
	m, _ = update(m, key("x"))
	if m.mode != modeConfirm {
		t.Fatalf("x on a running project should open a confirm modal")
	}
	if m.pending.op != opKillProject {
		t.Errorf("pending.op = %d, want opKillProject", m.pending.op)
	}
	if !strings.Contains(m.confirmPrompt, "on_project_exit") {
		t.Errorf("confirm should mention hooks, got %q", m.confirmPrompt)
	}
}
