package tmux

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// recordingRunner captures the argv of the last Run call and returns canned
// output/err, so tests can assert exactly which tmux command was issued.
type recordingRunner struct {
	args []string
	out  string
	err  error
}

func (r *recordingRunner) Run(args ...string) (string, error) {
	r.args = args
	return r.out, r.err
}

func TestCapturePane(t *testing.T) {
	r := &recordingRunner{out: "line one\nline two"}
	out, err := CapturePane(r, "%1")
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if want := "line one\nline two"; out != want {
		t.Errorf("CapturePane output = %q, want %q", out, want)
	}
	if got := strings.Join(r.args, " "); got != "capture-pane -e -p -t %1" {
		t.Errorf("CapturePane invoked tmux with %q, want %q", got, "capture-pane -e -p -t %1")
	}
}

func TestCapturePaneError(t *testing.T) {
	r := &recordingRunner{err: errors.New("boom"), out: "no such pane"}
	if _, err := CapturePane(r, "%9"); err == nil {
		t.Error("CapturePane with runner error: want error, got nil")
	}
}

func TestCapturePaneFull(t *testing.T) {
	r := &recordingRunner{out: "full scrollback\nline two"}
	out, err := CapturePaneFull(r, "%1")
	if err != nil {
		t.Fatalf("CapturePaneFull: %v", err)
	}
	if want := "full scrollback\nline two"; out != want {
		t.Errorf("CapturePaneFull output = %q, want %q", out, want)
	}
	if got := strings.Join(r.args, " "); got != "capture-pane -S - -E - -e -p -t %1" {
		t.Errorf("CapturePaneFull invoked tmux with %q, want %q", got, "capture-pane -S - -E - -e -p -t %1")
	}
}

func TestMutationArgv(t *testing.T) {
	cases := []struct {
		name string
		call func(Runner) error
		want string
	}{
		{"KillWindow", func(r Runner) error { return KillWindow(r, "@2") }, "kill-window -t @2"},
		{"KillPane", func(r Runner) error { return KillPane(r, "%3") }, "kill-pane -t %3"},
		{"RenameSession", func(r Runner) error { return RenameSession(r, "$1", "new name") }, "rename-session -t $1 -- new name"},
		{"RenameWindow", func(r Runner) error { return RenameWindow(r, "@2", "code") }, "rename-window -t @2 -- code"},
		{"SelectLayout", func(r Runner) error { return SelectLayout(r, "@2", "tiled") }, "select-layout -t @2 tiled"},
		{"SelectWindow", func(r Runner) error { return SelectWindow(r, "@2") }, "select-window -t @2"},
		{"SelectPane", func(r Runner) error { return SelectPane(r, "%3") }, "select-pane -t %3"},
		{"ZoomPane", func(r Runner) error { return ZoomPane(r, "%3") }, "resize-pane -Z -t %3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &recordingRunner{}
			if err := c.call(r); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if got := strings.Join(r.args, " "); got != c.want {
				t.Errorf("%s invoked tmux with %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestNewWindow(t *testing.T) {
	r := &recordingRunner{out: "@5|%9"}
	win, pane, err := NewWindow(r, "$1", "servers", "/tmp/proj")
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if win != "@5" || pane != "%9" {
		t.Errorf("NewWindow ids = %q,%q, want @5,%%9", win, pane)
	}
	want := "new-window -P -F #{window_id}|#{pane_id} -t $1 -n servers -c /tmp/proj"
	if got := strings.Join(r.args, " "); got != want {
		t.Errorf("NewWindow argv = %q, want %q", got, want)
	}
}

func TestNewWindowOmitsEmptyNameAndRoot(t *testing.T) {
	r := &recordingRunner{out: "@6|%10"}
	if _, _, err := NewWindow(r, "$1", "", ""); err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	want := "new-window -P -F #{window_id}|#{pane_id} -t $1"
	if got := strings.Join(r.args, " "); got != want {
		t.Errorf("NewWindow argv = %q, want %q (no -n/-c)", got, want)
	}
}

func TestNewWindowBadOutput(t *testing.T) {
	r := &recordingRunner{out: "no-delimiter"}
	if _, _, err := NewWindow(r, "$1", "x", ""); err == nil {
		t.Error("NewWindow with malformed output: want error, got nil")
	}
}

// TestIntegrationRenameToDashLeadingName drives a real tmux server, because the
// bug this guards is in tmux's argument parsing rather than in wyrm's: without
// the "--" terminator, "rename-window -t @0 -bad" is rejected as "unknown flag
// -b". A mock runner cannot show that, so this one talks to tmux.
func TestIntegrationRenameToDashLeadingName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test needs a real tmux")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	const socket = "wyrm-rename-test"
	r := Exec{ConfigFile: os.DevNull, SocketName: socket}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	out, err := r.Run("new-session", "-d", "-P", "-F", "#{session_id}|#{window_id}",
		"-s", "renametest", "-n", "w")
	if err != nil {
		t.Fatalf("new-session: %v (%s)", err, out)
	}
	sessionID, windowID, ok := strings.Cut(out, "|")
	if !ok {
		t.Fatalf("unexpected new-session output %q", out)
	}

	if err := RenameWindow(r, windowID, "-bad"); err != nil {
		t.Fatalf("RenameWindow to a dash-leading name: %v", err)
	}
	if err := RenameSession(r, sessionID, "-alsobad"); err != nil {
		t.Fatalf("RenameSession to a dash-leading name: %v", err)
	}

	got, err := r.Run("display-message", "-p", "-t", windowID, "-F", "#{session_name}|#{window_name}")
	if err != nil {
		t.Fatalf("display-message: %v (%s)", err, got)
	}
	if got != "-alsobad|-bad" {
		t.Errorf("names after rename = %q, want %q", got, "-alsobad|-bad")
	}
}
