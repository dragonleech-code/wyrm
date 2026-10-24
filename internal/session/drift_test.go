package session

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jskoll/wyrm/internal/config"
)

func TestPaneCount(t *testing.T) {
	cases := []struct {
		w    config.Window
		want int
	}{
		{config.Window{}, 1},
		{config.Window{Panes: []config.Pane{{}, {}, {}}}, 3},
		{config.Window{Splits: []config.Split{{Command: "a"}}}, 1},
		{config.Window{Splits: []config.Split{
			{Command: "a"},
			{Type: "h", Children: []config.Split{{Command: "b"}, {Type: "v"}}},
		}}, 3},
	}
	for i, c := range cases {
		if got := PaneCount(c.w); got != c.want {
			t.Errorf("case %d: PaneCount = %d, want %d", i, got, c.want)
		}
	}
}

func driftConfig() *config.Config {
	return &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{
			{Name: "code", Splits: []config.Split{{Command: "nvim"}, {Type: "h", Command: "make test"}}},
			{Name: "logs", Splits: []config.Split{{Command: "tail -f log"}}},
		},
	}
}

func TestDiff(t *testing.T) {
	r := &fakeRunner{
		listOutput:        "$1|proj",
		listWindowsOutput: "1|@1|1|layout|code\n2|@2|0|layout|scratch",
		listPanesOutput:   map[string]string{"@1": "%1|0|1|nvim|/tmp/proj"},
	}
	name, id, drift, err := Diff(r, driftConfig())
	if err != nil {
		t.Fatal(err)
	}
	if name != "proj" || id != "$1" {
		t.Errorf("name, id = %q, %q", name, id)
	}
	want := []WindowDrift{
		{Window: "code", Kind: DriftPanes, ConfigPanes: 2, LivePanes: 1},
		{Window: "logs", Kind: DriftMissing, ConfigPanes: 1},
		{Window: "scratch", Kind: DriftExtra},
	}
	if !slices.Equal(drift, want) {
		t.Errorf("drift = %+v\nwant %+v", drift, want)
	}
}

func TestDiffNotRunning(t *testing.T) {
	_, _, _, err := Diff(&fakeRunner{}, driftConfig())
	if !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("err = %v, want ErrSessionNotRunning", err)
	}
}

func TestReconcileAddsOnlyMissingWindows(t *testing.T) {
	r := &fakeRunner{
		listOutput:        "$1|proj",
		listWindowsOutput: "1|@1|1|layout|code",
		winSeq:            1,
		paneSeq:           2,
	}
	cfg := driftConfig()
	cfg.Session.OnProjectStart = "echo should-not-run"
	var stderr bytes.Buffer
	_, _, added, err := Reconcile(r, cfg, io.Discard, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"logs"}) {
		t.Errorf("added = %v, want [logs]", added)
	}
	want := []string{
		"list-sessions -F #{session_id}|#{session_name}",
		"list-windows -t $1 -F #{window_index}|#{window_id}|#{?window_active,1,0}|#{window_layout}|#{window_name}",
		"new-window -d -P -F #{window_id}|#{pane_id} -t $1 -n logs -c /tmp/proj",
		"show-options -w -v -t @2 synchronize-panes",
		"set-window-option -t @2 synchronize-panes off",
		"send-keys -t %3 -l -- tail -f log",
		"send-keys -t %3 Enter",
		"set-window-option -u -t @2 synchronize-panes",
	}
	if got := r.joined(); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(stderr.String(), "on_project_start") {
		t.Errorf("reconcile ran a lifecycle hook: %s", stderr.String())
	}
}

func TestWaitForGatesCommand(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", Splits: []config.Split{
			{Command: "psql", WaitFor: "tcp:localhost:5432", WaitTimeout: "30s"},
			{Type: "h", Run: "npm run dev", WaitFor: "file:/tmp/ready"},
		}}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard, WithWaitCommand("/bin/wyrm")); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.joined(), "\n")
	for _, want := range []string{
		"send-keys -t %1 -l -- '/bin/wyrm' wait -timeout 30s 'tcp:localhost:5432' && psql",
		"respawn-pane -k -t %2 -c /tmp/proj -- '/bin/wyrm' wait -timeout 2m0s 'file:/tmp/ready' && npm run dev",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWaitForWithoutBinaryWarns(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", Splits: []config.Split{{Command: "psql", WaitFor: "tcp::5432"}}}},
	}
	r := &fakeRunner{}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr, WithWaitCommand("/odd'path/wyrm")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.joined(), "\n"), "send-keys -t %1 -l -- psql") {
		t.Errorf("command not sent ungated: %v", r.joined())
	}
	if !strings.Contains(stderr.String(), "without waiting") {
		t.Errorf("no warning: %q", stderr.String())
	}
}

func TestSkipHooks(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", OnProjectExit: "exit 1"},
		Windows: []config.Window{{Name: "w"}},
	}
	var ran []string
	runner := func(spec HookSpec) error { ran = append(ran, spec.Label); return nil }
	r := &fakeRunner{listOutput: "$1|proj"}
	if _, err := Kill(r, cfg, io.Discard, SkipHooks(), WithHookRunner(runner)); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("hooks ran despite SkipHooks: %v", ran)
	}
	r = &fakeRunner{listOutput: "$1|proj"}
	if _, err := Kill(r, cfg, io.Discard, WithHookRunner(runner)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"on_project_exit"}) {
		t.Errorf("ran = %v, want on_project_exit", ran)
	}
}

func TestHookRunnerReceivesSpec(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj", Root: "/tmp/proj", OnProjectStart: "make setup",
			HookTimeout: "90s", Env: map[string]string{"B": "2", "A": "1"},
		},
		Windows: []config.Window{{Name: "w"}},
	}
	var got HookSpec
	runner := func(spec HookSpec) error { got = spec; return nil }
	if _, _, _, err := Create(&fakeRunner{}, cfg, io.Discard, io.Discard, WithHookRunner(runner)); err != nil {
		t.Fatal(err)
	}
	if got.Command != "make setup" || got.Dir != "/tmp/proj" || got.Timeout != 90*time.Second {
		t.Errorf("spec = %+v", got)
	}
	if !slices.Equal(got.Env, []string{"A=1", "B=2"}) {
		t.Errorf("env = %v", got.Env)
	}
}

func TestRunLocalHookTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a shell")
	}
	start := time.Now()
	err := RunLocalHook(HookSpec{Command: "sleep 30", Dir: t.TempDir(), Timeout: 200 * time.Millisecond, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("timeout took %s", time.Since(start))
	}
}
