package main

import (
	"bytes"
	"strings"
	"testing"
)

type sendTestRunner struct {
	calls []string
}

func (r *sendTestRunner) Run(args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	r.calls = append(r.calls, cmd)
	if args[0] == "list-sessions" {
		return "$1|myproj\n$2|other", nil
	}
	if args[0] == "list-windows" {
		return "0|@1|1|layout|editor\n1|@2|0|layout|server\n2|@3|0|layout|api.v2", nil
	}
	if args[0] == "list-panes" {
		return "%10|0|1|bash\n%11|1|0|node", nil
	}
	return "", nil
}

func TestSendValidation(t *testing.T) {
	t.Run("no args", func(t *testing.T) {
		r := &sendTestRunner{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"send"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "target required") {
			t.Errorf("stderr = %q, want target required", stderr.String())
		}
	})

	t.Run("target without command", func(t *testing.T) {
		r := &sendTestRunner{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"send", "myproj"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 2 {
			t.Errorf("code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "command or keys required") {
			t.Errorf("stderr = %q, want command or keys required", stderr.String())
		}
	})
}

func TestSendExecution(t *testing.T) {
	t.Run("dotted window name", func(t *testing.T) {
		r := &sendTestRunner{}
		id, err := resolveSendTarget(r, "myproj:api.v2")
		if err != nil || id != "@3" {
			t.Fatalf("target = %q, %v; want @3", id, err)
		}
	})
	t.Run("default send appends Enter", func(t *testing.T) {
		r := &sendTestRunner{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"send", "myproj:server", "npm", "test"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		// Expects send-keys -t @2 -l -- npm test and send-keys -t @2 Enter
		var foundLiteral, foundEnter bool
		for _, c := range r.calls {
			if c == "send-keys -t @2 -l -- npm test" {
				foundLiteral = true
			}
			if c == "send-keys -t @2 Enter" {
				foundEnter = true
			}
		}
		if !foundLiteral || !foundEnter {
			t.Errorf("calls = %v, want literal and enter calls", r.calls)
		}
	})

	t.Run("send with no-enter", func(t *testing.T) {
		r := &sendTestRunner{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"send", "-n", "myproj:editor.1", "foo"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		var foundLiteral, foundEnter bool
		for _, c := range r.calls {
			if c == "send-keys -t %11 -l -- foo" {
				foundLiteral = true
			}
			if strings.Contains(c, "Enter") {
				foundEnter = true
			}
		}
		if !foundLiteral || foundEnter {
			t.Errorf("calls = %v, want literal and NO enter call", r.calls)
		}
	})

	t.Run("send raw keys", func(t *testing.T) {
		r := &sendTestRunner{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"send", "-r", "-n", "%99", "C-c"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 0 {
			t.Fatalf("code = %d, stderr = %q", code, stderr.String())
		}
		found := false
		for _, c := range r.calls {
			if c == "send-keys -t %99 C-c" {
				found = true
			}
		}
		if !found {
			t.Errorf("calls = %v, want send-keys -t %%99 C-c", r.calls)
		}
	})

	t.Run("unknown session error", func(t *testing.T) {
		r := &sendTestRunner{}
		var stdout, stderr bytes.Buffer
		code := run([]string{"send", "nonexistent", "ls"}, &stdout, &stderr, r, func() bool { return false }, nil)
		if code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "no running session named \"nonexistent\"") {
			t.Errorf("stderr = %q, want session not found", stderr.String())
		}
	})
}

// tmux strips a trailing ";" from an argument as its command separator, so
// `wyrm send db "SELECT 1;"` typed the query and never terminated it. The
// statement then sat unexecuted in the REPL — the failure is invisible in a
// shell, where a trailing ";" is a no-op, and total in psql or sqlite3.
func TestSendPreservesTrailingSemicolon(t *testing.T) {
	r := &sendTestRunner{}
	var stdout, stderr bytes.Buffer
	code := run([]string{"send", "myproj", "SELECT 1;"}, &stdout, &stderr, r, func() bool { return false }, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}

	var sent string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "send-keys ") && strings.Contains(c, " -l -- ") {
			sent = c[strings.Index(c, " -l -- ")+len(" -l -- "):]
		}
	}
	if sent == "" {
		t.Fatalf("no literal send-keys issued; calls = %v", r.calls)
	}
	if !strings.HasPrefix(sent, "SELECT 1;") {
		t.Errorf("sent %q, want the semicolon preserved", sent)
	}
}
