package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// loopRunner is a Runner with no batching, to exercise the fallback path.
type loopRunner struct {
	calls [][]string
	fail  map[int]bool // index of calls that should fail
	out   map[int]string
}

func (l *loopRunner) Run(args ...string) (string, error) {
	i := len(l.calls)
	l.calls = append(l.calls, args)
	if l.fail[i] {
		return "boom", errors.New("exit status 1")
	}
	return l.out[i], nil
}

func TestRunEachFallsBackWithoutBatching(t *testing.T) {
	r := &loopRunner{fail: map[int]bool{1: true}}
	errs := RunEach(r, [][]string{{"a"}, {"b"}, {"c"}})
	if len(r.calls) != 3 {
		t.Fatalf("issued %d calls, want one per command: %v", len(r.calls), r.calls)
	}
	if errs[0] != nil || errs[2] != nil {
		t.Errorf("errs = %v, want only the middle one set", errs)
	}
	if errs[1] == nil {
		t.Error("the failing command reported no error")
	}
	// The whole point of the policy: a failure does not cancel what follows.
	if got := strings.Join(r.calls[2], " "); got != "c" {
		t.Errorf("third call = %q, want it to have run anyway", got)
	}
}

func TestRunEachEmpty(t *testing.T) {
	r := &loopRunner{}
	if errs := RunEach(r, nil); len(errs) != 0 {
		t.Errorf("errs = %v, want empty", errs)
	}
	if len(r.calls) != 0 {
		t.Errorf("issued %v for an empty batch", r.calls)
	}
}

// batchStub reports that the first n commands succeeded, then failed.
type batchStub struct {
	loopRunner
	completed int
	batches   int
}

func (b *batchStub) RunBatch(cmds [][]string) ([]string, error) {
	b.batches++
	if b.completed >= len(cmds) {
		return make([]string, len(cmds)), nil
	}
	return make([]string, b.completed), errors.New("exit status 1")
}

// The happy path is a single process and no per-command calls at all.
func TestRunEachBatchesWhenSupported(t *testing.T) {
	b := &batchStub{completed: 3}
	errs := RunEach(b, [][]string{{"a"}, {"b"}, {"c"}})
	if b.batches != 1 {
		t.Errorf("issued %d batches, want 1", b.batches)
	}
	if len(b.calls) != 0 {
		t.Errorf("fell back to individual calls: %v", b.calls)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("errs[%d] = %v, want nil", i, err)
		}
	}
}

// The replay must resume at the failure, never re-issuing what already ran —
// re-typing a send-keys would be worse than the failure it recovers from.
func TestRunEachReplaysOnlyFromTheFailure(t *testing.T) {
	b := &batchStub{completed: 2} // a, b ran; c failed
	b.fail = map[int]bool{0: true}
	errs := RunEach(b, [][]string{{"a"}, {"b"}, {"c"}, {"d"}})

	if b.batches != 1 {
		t.Errorf("issued %d batches, want 1", b.batches)
	}
	var replayed []string
	for _, c := range b.calls {
		replayed = append(replayed, strings.Join(c, " "))
	}
	want := []string{"c", "d"}
	if len(replayed) != len(want) {
		t.Fatalf("replayed %v, want exactly %v — a and b already took effect", replayed, want)
	}
	for i := range want {
		if replayed[i] != want[i] {
			t.Errorf("replayed[%d] = %q, want %q", i, replayed[i], want[i])
		}
	}
	if errs[0] != nil || errs[1] != nil {
		t.Errorf("errs = %v, want the batched-and-succeeded commands unmarked", errs)
	}
	if errs[2] == nil {
		t.Error("the failing command reported no error after replay")
	}
	if errs[3] != nil {
		t.Errorf("errs[3] = %v, want nil — it ran fine on replay", errs[3])
	}
}

// TestRunBatchCommand guards RunBatch honoring Exec.Command the same way Run
// does — a wrapper/fork binary needs every code path to invoke it, not just
// single commands. The fake script echoes the value following every "-p"
// flag on its own line, which is enough to stand in for tmux's own
// "display-message -p" that RunBatch relies on for both a command's own
// output and its trailing marker.
func TestRunBatchCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux script requires a POSIX shell")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"prev=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"-p\" ]; then printf '%s\\n' \"$a\"; fi\n" +
		"  prev=\"$a\"\n" +
		"done\n"
	path := filepath.Join(dir, "custom-tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := Exec{Command: path}
	results, err := r.RunBatch([][]string{{"display-message", "-p", "one"}})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if len(results) != 1 || results[0] != "one" {
		t.Errorf("results = %q, want [\"one\"]", results)
	}
}

func TestSplitOnMarker(t *testing.T) {
	const m = "MARK"
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"empty", "", nil},
		{"no output per command", m + "\n" + m, []string{"", ""}},
		{"one line each", "a\n" + m + "\nb\n" + m, []string{"a", "b"}},
		{"multi-line", "a1\na2\n" + m + "\nb1\n" + m, []string{"a1\na2", "b1"}},
		// Trailing text belongs to a command that never finished: dropped.
		{"unfinished tail", "a\n" + m + "\npartial", []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitOnMarker(tt.out, m)
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestIntegrationRunBatch drives a real tmux: the batch has to run in one
// process, return each command's own output, and — on a mid-batch failure —
// report exactly how many commands got through.
func TestIntegrationRunBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-batch-it-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })
	if out, err := r.Run("new-session", "-d", "-s", "b", "-n", "w"); err != nil {
		t.Fatalf("new-session: %v (%s)", err, out)
	}

	// Outputs come back per command, in order.
	results, err := r.RunBatch([][]string{
		{"display-message", "-p", "one"},
		{"display-message", "-p", "two"},
		{"list-windows", "-t", "b", "-F", "w:#{window_name}"},
	})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	want := []string{"one", "two", "w:w"}
	if len(results) != len(want) {
		t.Fatalf("results = %q, want %q", results, want)
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("results[%d] = %q, want %q", i, results[i], want[i])
		}
	}

	// A command producing no output still occupies its slot, so indices stay
	// aligned with the commands that produced them.
	results, err = r.RunBatch([][]string{
		{"send-keys", "-t", "b", "-l", "--", "x"},
		{"display-message", "-p", "after"},
	})
	if err != nil {
		t.Fatalf("RunBatch with a silent command: %v", err)
	}
	if len(results) != 2 || results[0] != "" || results[1] != "after" {
		t.Errorf("results = %q, want [\"\" \"after\"]", results)
	}

	// Mid-batch failure: tmux abandons the rest, and the short result says where.
	results, err = r.RunBatch([][]string{
		{"display-message", "-p", "ran"},
		{"kill-pane", "-t", "%999"},
		{"display-message", "-p", "never"},
	})
	if err == nil {
		t.Fatal("RunBatch with a bad command returned no error")
	}
	if len(results) != 1 {
		t.Errorf("results = %q, want exactly the 1 command that completed", results)
	}
	if !strings.Contains(err.Error(), "%999") {
		t.Errorf("err = %v, want tmux's own diagnostic", err)
	}
}

// No server running has to survive batching as the ordinary outcome it is.
func TestIntegrationRunBatchNoServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	r := Exec{SocketName: fmt.Sprintf("wyrm-batch-none-%d", os.Getpid())}
	_, err := r.RunBatch([][]string{{"list-sessions"}})
	if err == nil {
		t.Fatal("want an error with no server running")
	}
	if !errors.Is(err, ErrNoServer) {
		t.Errorf("err = %v, want it to wrap ErrNoServer", err)
	}
}
