package tmux

import (
	"slices"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":        "'plain'",
		"it's":         `'it'\''s'`,
		"$HOME; rm -r": "'$HOME; rm -r'",
		"":             "''",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSSHArgs(t *testing.T) {
	s := SSH{Host: "devbox", SocketName: "work"}
	remote := QuoteArgs(s.tmuxArgs([]string{"new-session", "-s", "a b"}))
	if remote != "'tmux' '-L' 'work' 'new-session' '-s' 'a b'" {
		t.Errorf("remote = %s", remote)
	}
	args := s.sshArgs(remote, false)
	if !slices.Contains(args, "BatchMode=yes") || !slices.Contains(args, "-T") {
		t.Errorf("batch args = %v", args)
	}
	if args[len(args)-3] != "devbox" || args[len(args)-2] != "--" || args[len(args)-1] != remote {
		t.Errorf("tail = %v", args[len(args)-3:])
	}
	tty := s.sshArgs(remote, true)
	if tty[0] != "-t" || slices.Contains(tty, "BatchMode=yes") {
		t.Errorf("tty args = %v", tty)
	}
}

func TestShellCommand(t *testing.T) {
	got := ShellCommand("make 'deps'", "/srv/my app", []string{"A=1"})
	want := `cd '/srv/my app' && env 'A=1' sh -c 'make '\''deps'\'''`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(ShellCommand("x", "/", nil), "env") {
		t.Error("env with no vars")
	}
}
