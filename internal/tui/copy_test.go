package tui

import (
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/clipboard"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// fakeClipboard swaps in a recording clipboard for the duration of a test, so
// what `y` copies can be asserted directly instead of inferred from the status
// message — and so the test does not depend on the host having a clipboard
// tool. It returns a pointer to the last text written.
func fakeClipboard(t *testing.T, err error) *string {
	t.Helper()
	var got string
	prev := clipboardWrite
	clipboardWrite = func(text string) error {
		got = text
		return err
	}
	t.Cleanup(func() { clipboardWrite = prev })
	return &got
}

func completeCopy(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("copy did not return an asynchronous command")
	}
	next, _ = next.Update(cmd())
	return next.(Model)
}

func TestCopyKeyInPanels(t *testing.T) {
	copied := fakeClipboard(t, nil)

	m := New(nopRunner(), nil)
	m.width, m.height, m.ready = 100, 30, true
	m.sessions = []sessions.Session{{ID: "$1", Name: "my-session"}}
	m.cur[panelSessions] = 0
	m.focus = panelSessions

	m = completeCopy(t, m)
	if *copied != "my-session" {
		t.Errorf("copied %q, want the session name", *copied)
	}
	if !strings.Contains(m.info, "my-session") {
		t.Errorf("expected info message mentioning my-session, got %q", m.info)
	}

	// Any key clears info message
	m, _ = update(m, key("j"))
	if m.info != "" {
		t.Errorf("info should be cleared on keypress, got %q", m.info)
	}

	// Copy project path
	m.projects = []Project{{Name: "proj", Path: "/path/to/proj"}}
	m.cur[panelProjects] = 0
	m.focus = panelProjects
	m = completeCopy(t, m)
	if *copied != "/path/to/proj" {
		t.Errorf("copied %q, want the project path", *copied)
	}
	if !strings.Contains(m.info, "/path/to/proj") {
		t.Errorf("expected info mentioning /path/to/proj, got %q", m.info)
	}

	// Copy window name
	m.windows = []tmux.WindowInfo{{ID: "@1", Name: "code"}}
	m.cur[panelWindows] = 0
	m.focus = panelWindows
	m = completeCopy(t, m)
	if *copied != "code" {
		t.Errorf("copied %q, want the window name", *copied)
	}
	if !strings.Contains(m.info, "code") {
		t.Errorf("expected info mentioning code, got %q", m.info)
	}
}

// `y` used to claim "copied to clipboard" whether or not anything was copied.
// On a box with no clipboard tool the message has to say so, and name what to
// install — this is the common case on a bare server.
func TestCopyReportsMissingBackend(t *testing.T) {
	fakeClipboard(t, clipboard.ErrNoBackend)

	m := New(nopRunner(), nil)
	m.width, m.height, m.ready = 100, 30, true
	m.sessions = []sessions.Session{{ID: "$1", Name: "my-session"}}
	m.cur[panelSessions] = 0
	m.focus = panelSessions

	m = completeCopy(t, m)
	if strings.Contains(m.info, "copied") {
		t.Errorf("nothing was copied, so nothing should claim it was: %q", m.info)
	}
	if !strings.Contains(m.info, clipboard.Backends()) {
		t.Errorf("want the installable tools named, got %q", m.info)
	}
}

// A backend that exists but fails names the panel item, so the user knows which
// copy failed rather than just that one did.
func TestCopyReportsBackendFailure(t *testing.T) {
	fakeClipboard(t, errors.New("exit status 1"))

	m := New(nopRunner(), nil)
	m.width, m.height, m.ready = 100, 30, true
	m.sessions = []sessions.Session{{ID: "$1", Name: "my-session"}}
	m.cur[panelSessions] = 0
	m.focus = panelSessions

	m = completeCopy(t, m)
	if !strings.Contains(m.info, "exit status 1") {
		t.Errorf("want the underlying error surfaced, got %q", m.info)
	}
	if strings.Contains(m.info, "copied ") && !strings.Contains(m.info, "cannot copy") {
		t.Errorf("a failure should not read as a success: %q", m.info)
	}
}
