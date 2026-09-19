package tui

import (
	"bytes"
	"testing"

	"github.com/jskoll/wyrm/internal/sessions"
)

func TestSessionEntriesSanitizedAndStaleProjectSnapshot(t *testing.T) {
	m := New(nopRunner(), nil)
	m.allSessions = true
	m.sessions = []sessions.Session{{ID: "$1", Name: "a_b"}}
	m.projects = []Project{{Name: "a.b"}}
	entries := m.sessionEntries()
	if len(entries) != 1 || !entries[0].HasProject {
		t.Fatalf("sanitized project duplicated: %+v", entries)
	}
	m.sessions = nil
	m.projects[0].Running, m.projects[0].SessionID = true, "$1"
	entries = m.sessionEntries()
	if len(entries) != 1 || entries[0].Running {
		t.Fatalf("stale snapshot hid stopped project: %+v", entries)
	}
}

func TestCopyIsDeferredAndFilelessProjectUsesRoot(t *testing.T) {
	copied := fakeClipboard(t, nil)
	m := New(nopRunner(), nil)
	m.focus = panelProjects
	m.projects = []Project{{Name: "project", Root: "/work/project"}}
	next, cmd := m.Update(key("y"))
	if *copied != "" {
		t.Fatal("clipboard was invoked in Update")
	}
	if cmd == nil {
		t.Fatal("no asynchronous copy command")
	}
	next, _ = next.Update(cmd())
	if *copied != "/work/project" || next.(Model).info == "" {
		t.Fatal("fileless project was not copied")
	}
}

func TestNotificationTerminalOutputAndScanMetadata(t *testing.T) {
	var out bytes.Buffer
	c := &terminalOutput{text: "\a\x1b]9;ready\x1b\\"}
	c.SetStdout(&out)
	if err := c.Run(); err != nil || out.String() != c.text {
		t.Fatalf("terminal delivery: %q %v", out.String(), err)
	}
	r := agentRunner(pl("$1", "@1", "%1", "claude"), map[string]string{"%1": waitingPane})
	msg := loadAgentStatus(r, nil, "")().(agentStatusMsg)
	ref := msg.status.refs["%1"]
	if ref.SessionName != "s" || ref.WindowName != "w" {
		t.Fatalf("scan lost notification metadata: %+v", ref)
	}
}
