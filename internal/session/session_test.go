package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

// fakeRunner records every tmux invocation and fabricates the outputs the
// real tmux would print for -P -F commands, handing out sequential window
// (@N) and pane (%N) IDs. "list-sessions" (used by tmux.FindSessionID to
// check whether a session is already running) returns listOutput verbatim,
// in the "id|name" format FindSessionID expects; empty means "not running".
type fakeRunner struct {
	calls   [][]string
	winSeq  int
	paneSeq int

	// sessionName is what new-session reports back as #{session_name}. Empty
	// means "whatever was asked for", i.e. the -s argument — the normal case.
	// Setting it simulates the tmux builds that rewrite "." and ":" to "_".
	sessionName string
	// lastSessionName is what the most recent new-session call resolved to,
	// echoed back by the display-message lookup newSession follows up with —
	// see tmux.SessionName.
	lastSessionName string

	// fail forces the named command (args[0]) to return an error.
	fail map[string]bool
	// badNewSessionOutput makes new-session/new-window return output with
	// no "|" separator, exercising the "unexpected tmux output" path.
	badNewSessionOutput bool
	// listOutput is returned verbatim for "list-sessions" calls.
	listOutput string
	// listWindowsOutput backs "list-windows" (selectStartup's window lookup),
	// in the id-ordered "index|@id|active|layout|name" format ListWindows parses.
	listWindowsOutput string
	// listPanesOutput backs "list-panes", keyed by the -t target (a window ID
	// such as "@2"), in the "%id|index|active|command" format ListPanes parses.
	listPanesOutput map[string]string
	// windowSync backs "show-options ... synchronize-panes": the window's own
	// value, empty meaning it has none and inherits the global one.
	windowSync string
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.fail[args[0]] {
		return "boom", errors.New("exit status 1")
	}
	switch args[0] {
	case "new-session":
		f.winSeq++
		f.paneSeq++
		if f.badNewSessionOutput {
			return "malformed", nil
		}
		name := f.sessionName
		if name == "" {
			for i, a := range args {
				if a == "-s" && i+1 < len(args) {
					name = args[i+1]
				}
			}
		}
		f.lastSessionName = name
		return fmt.Sprintf("$1|@%d|%%%d", f.winSeq, f.paneSeq), nil
	case "new-window":
		f.winSeq++
		f.paneSeq++
		if f.badNewSessionOutput {
			return "malformed", nil
		}
		return fmt.Sprintf("@%d|%%%d", f.winSeq, f.paneSeq), nil
	case "split-window":
		f.paneSeq++
		return fmt.Sprintf("%%%d", f.paneSeq), nil
	case "display-message":
		// newSession's follow-up lookup of the session's real name — see
		// tmux.SessionName.
		return f.lastSessionName, nil
	case "list-sessions":
		return f.listOutput, nil
	case "list-windows":
		return f.listWindowsOutput, nil
	case "list-panes":
		return f.listPanesOutput[args[2]], nil
	case "show-options":
		return f.windowSync, nil
	}
	return "", nil
}

// joined flattens recorded calls for order-sensitive assertions.
func (f *fakeRunner) joined() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = strings.Join(c, " ")
	}
	return out
}

func TestCreateSplitTree(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name:      "editor",
			PreWindow: "nvm use 18",
			Splits: []config.Split{
				{Command: "nvim"},
				{Type: "h", Size: 30, Command: "npm run dev", Children: []config.Split{
					{Type: "v", Command: "npm test"},
				}},
			},
		}},
	}

	r := &fakeRunner{}
	name, sessionID, created, err := Create(r, cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "proj" {
		t.Errorf("name = %q, want proj", name)
	}
	if sessionID != "$1" {
		t.Errorf("sessionID = %q, want $1", sessionID)
	}
	if !created {
		t.Error("created = false, want true")
	}

	// Note two orderings here.
	//
	// Both siblings at the top level are created before the nested child, so
	// the child splits %2 at its full size rather than after a later sibling
	// has already carved it up.
	//
	// And every pane is created before anything is typed: commands are
	// collected during the walk and issued together at the end, which lets a
	// batching Runner send them in one tmux process (see keyBatch). This mock
	// doesn't batch, so they appear individually — the commands and their
	// targets are unchanged either way, only the grouping moved.
	//
	// pre_window is typed once per pane, and every command goes through
	// "send-keys -l --" plus a separate Enter so a command that looks like a
	// key name is still typed.
	want := []string{
		"list-sessions -F #{session_id}|#{session_name}",
		"new-session -d -P -F #{session_id}|#{window_id}|#{pane_id} -s proj -n editor -c /tmp/proj",
		// tmux doesn't always name a session what was asked for, so the real
		// name is queried separately from the IDs above — see tmux.SessionName.
		"display-message -p -t $1 -F #{session_name}",
		// only the window's own value (no -A), so an inherited one is
		// restored by unsetting rather than pinned onto the window
		"show-options -w -v -t @1 synchronize-panes",
		"set-window-option -t @1 synchronize-panes off",
		// second entry splits the initial pane %1 -> %2 (breadth first)
		"split-window -d -t %1 -h -P -F #{pane_id} -c /tmp/proj -l 30%",
		// child splits its parent %2 -> %3
		"split-window -d -t %2 -v -P -F #{pane_id} -c /tmp/proj",
		// now the collected commands, in walk order
		"send-keys -t %1 -l -- nvm use 18",
		"send-keys -t %1 Enter",
		"send-keys -t %1 -l -- nvim",
		"send-keys -t %1 Enter",
		"send-keys -t %2 -l -- nvm use 18",
		"send-keys -t %2 Enter",
		"send-keys -t %2 -l -- npm run dev",
		"send-keys -t %2 Enter",
		"send-keys -t %3 -l -- nvm use 18",
		"send-keys -t %3 Enter",
		"send-keys -t %3 -l -- npm test",
		"send-keys -t %3 Enter",
		// the window had no synchronize-panes of its own: unset, not "off"
		"set-window-option -u -t @1 synchronize-panes",
		// no startup_window: land on the first window explicitly
		"select-window -t @1",
	}
	got := r.joined()
	if len(got) != len(want) {
		t.Fatalf("got %d calls, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestCreateLegacyPanes(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name: "tests",
			Panes: []config.Pane{
				{Command: "npm test"},
				{Command: "npm run lint"},
				{Command: "# placeholder"},
			},
		}},
	}

	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := strings.Join(r.joined(), "\n")
	for _, want := range []string{
		"send-keys -t %1 -l -- npm test",
		"split-window -d -t %1 -h -P -F #{pane_id} -c /tmp/proj",
		"send-keys -t %2 -l -- npm run lint",
		"split-window -d -t %2 -v -P -F #{pane_id} -c /tmp/proj",
		"select-layout -t @1 tiled",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing call %q in:\n%s", want, got)
		}
	}
	// The comment command must not be typed into pane %3.
	if strings.Contains(got, "placeholder") {
		t.Errorf("comment command was sent:\n%s", got)
	}
}

func TestCreateMultipleWindowsAndStartup(t *testing.T) {
	pane := 1
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", StartupWindow: "second", StartupPane: &pane},
		Windows: []config.Window{
			{Name: "first"},
			{Name: "second"},
		},
	}

	// selectStartup resolves the window/pane to their tmux IDs via
	// list-windows/list-panes: window "second" is @2, and its pane at index 1
	// is %2.
	r := &fakeRunner{
		listWindowsOutput: "0|@1|0|layout|first\n1|@2|1|layout|second",
		listPanesOutput:   map[string]string{"@2": "%2|1|1|zsh|/tmp/proj"},
	}
	_, sessionID, _, err := Create(r, cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := strings.Join(r.joined(), "\n")
	for _, want := range []string{
		"new-session -d -P -F #{session_id}|#{window_id}|#{pane_id} -s proj -n first -c /tmp/proj",
		"new-window -d -P -F #{window_id}|#{pane_id} -t " + sessionID + " -n second -c /tmp/proj",
		"select-window -t @2",
		"select-pane -t %2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing call %q in:\n%s", want, got)
		}
	}
}

// TestCreateStartupWindowWithDot guards against the "." misparse: a window
// named "app.web" must be targeted by its @id, never by "session:app.web"
// (which tmux would read as window "app", pane "web").
func TestCreateStartupWindowWithDot(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", StartupWindow: "app.web"},
		Windows: []config.Window{{Name: "app.web"}},
	}
	r := &fakeRunner{listWindowsOutput: "0|@1|1|layout|app.web"}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := strings.Join(r.joined(), "\n")
	if !strings.Contains(got, "select-window -t @1") {
		t.Errorf("want select-window targeting the window ID @1, got:\n%s", got)
	}
	for _, c := range r.calls {
		for i, arg := range c {
			if i > 0 && c[i-1] == "-t" && strings.Contains(arg, "app.web") {
				t.Errorf("call %v targets the raw dotted window name instead of an ID", c)
			}
		}
	}
}

func TestCreateDerivesNameFromRoot(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Root: "/tmp/derived-name"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	name, _, _, err := Create(r, cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if name != "derived-name" {
		t.Errorf("name = %q, want derived-name", name)
	}
}

func TestCreateRequiresWindows(t *testing.T) {
	cfg := &config.Config{Session: config.Session{Name: "x"}}
	if _, _, _, err := Create(&fakeRunner{}, cfg, io.Discard, io.Discard); err == nil {
		t.Error("Create with no windows: want error, got nil")
	}
}

func TestCreateLeavesRunningSessionUntouched(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", Splits: []config.Split{{Command: "nvim"}}}},
	}

	r := &fakeRunner{listOutput: "$9|proj"}
	name, sessionID, created, err := Create(r, cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "proj" {
		t.Errorf("name = %q, want proj", name)
	}
	if sessionID != "$9" {
		t.Errorf("sessionID = %q, want $9", sessionID)
	}
	if created {
		t.Error("created = true, want false for a running session")
	}
	got := r.joined()
	if len(got) != 1 || got[0] != "list-sessions -F #{session_id}|#{session_name}" {
		t.Errorf("running session must only be probed, got calls:\n%s", strings.Join(got, "\n"))
	}
}

// TestCreateLeavesRunningSessionUntouchedDottedName guards against the bug
// where a session name containing "." (e.g. "wyrm.vim") couldn't be found by
// name: tmux's -t target syntax misparses such names, so the existence check
// must match by comparing session_name strings in Go (via
// tmux.FindSessionID), not by passing the name through -t.
func TestCreateLeavesRunningSessionUntouchedDottedName(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "wyrm.vim", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w"}},
	}

	r := &fakeRunner{listOutput: "$4|wyrm.vim"}
	name, sessionID, created, err := Create(r, cfg, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "wyrm.vim" || sessionID != "$4" || created {
		t.Errorf("Create = %q, %q, %v; want wyrm.vim, $4, false", name, sessionID, created)
	}
}

func TestKill(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w"}},
	}

	t.Run("not running", func(t *testing.T) {
		r := &fakeRunner{}
		if _, err := Kill(r, cfg, io.Discard); err == nil {
			t.Fatal("want error for missing session")
		}
		for _, c := range r.joined() {
			if strings.HasPrefix(c, "kill-session") {
				t.Errorf("kill-session called for missing session: %q", c)
			}
		}
	})

	t.Run("running", func(t *testing.T) {
		r := &fakeRunner{listOutput: "$7|proj"}
		name, err := Kill(r, cfg, io.Discard)
		if err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if name != "proj" {
			t.Errorf("name = %q, want proj", name)
		}
		got := strings.Join(r.joined(), "\n")
		if !strings.Contains(got, "kill-session -t $7") {
			t.Errorf("missing kill-session call:\n%s", got)
		}
	})

	t.Run("on_project_exit failure still kills session", func(t *testing.T) {
		exitCfg := &config.Config{
			Session: config.Session{Name: "proj", Root: "/tmp/proj", OnProjectExit: "exit 1"},
			Windows: []config.Window{{Name: "w"}},
		}
		r := &fakeRunner{listOutput: "$7|proj"}
		var stderr bytes.Buffer
		name, err := Kill(r, exitCfg, &stderr)
		if err != nil {
			t.Fatalf("Kill: %v", err)
		}
		if name != "proj" {
			t.Errorf("name = %q, want proj", name)
		}
		if !strings.Contains(stderr.String(), "on_project_exit failed") {
			t.Errorf("stderr = %q, want on_project_exit failure warning", stderr.String())
		}
	})

	t.Run("kill-session error", func(t *testing.T) {
		r := &fakeRunner{listOutput: "$7|proj", fail: map[string]bool{"kill-session": true}}
		if _, err := Kill(r, cfg, io.Discard); err == nil || !strings.Contains(err.Error(), "killing session") {
			t.Errorf("Kill error = %v, want containing %q", err, "killing session")
		}
	})
}

func TestCreateOnProjectStartFailureStillCreates(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", OnProjectStart: "exit 1"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	var stderr bytes.Buffer
	name, _, created, err := Create(r, cfg, io.Discard, &stderr)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "proj" || !created {
		t.Errorf("name, created = %q, %v; want proj, true", name, created)
	}
	if !strings.Contains(stderr.String(), "on_project_start failed") {
		t.Errorf("stderr = %q, want on_project_start failure warning", stderr.String())
	}
}

// Create must never run on_project_attach: it cannot tell whether its caller
// is about to attach, and `up -d`, `restart -d` and `restart -all` do not.
// Callers run it immediately before handing the terminal over — see
// app.attachSession.
func TestCreateDoesNotRunAttachHook(t *testing.T) {
	cfg := func() *config.Config {
		return &config.Config{
			Session: config.Session{Name: "proj", Root: "/tmp/proj", OnProjectAttach: "echo attaching"},
			Windows: []config.Window{{Name: "w"}},
		}
	}

	t.Run("fresh build", func(t *testing.T) {
		r := &fakeRunner{}
		var stderr bytes.Buffer
		_, _, created, err := Create(r, cfg(), io.Discard, &stderr)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !created {
			t.Errorf("created = false, want true")
		}
		if strings.Contains(stderr.String(), "on_project_attach") {
			t.Errorf("Create ran on_project_attach; stderr = %q", stderr.String())
		}
	})

	t.Run("existing session", func(t *testing.T) {
		r := &fakeRunner{listOutput: "$1|proj"}
		var stderr bytes.Buffer
		_, _, created, err := Create(r, cfg(), io.Discard, &stderr)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created {
			t.Errorf("created = true, want false for already running session")
		}
		if strings.Contains(stderr.String(), "on_project_attach") {
			t.Errorf("Create ran on_project_attach; stderr = %q", stderr.String())
		}
	})

	t.Run("RunAttachHook still runs it", func(t *testing.T) {
		var stderr bytes.Buffer
		if err := RunAttachHook(cfg(), &stderr); err != nil {
			t.Fatalf("RunAttachHook: %v", err)
		}
		if !strings.Contains(stderr.String(), "running on_project_attach") {
			t.Errorf("stderr = %q, want running on_project_attach", stderr.String())
		}
	})
}

func TestCreateOnProjectDetachHook(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", OnProjectDetach: "echo detaching"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	var stderr bytes.Buffer
	_, _, created, err := Create(r, cfg, io.Discard, &stderr)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Errorf("created = false, want true")
	}
	found := false
	for _, call := range r.calls {
		if len(call) >= 4 && call[0] == "set-hook" && call[3] == "client-detached" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected set-hook client-detached call, got %v", r.calls)
	}
}

func TestRunAttachHookDryRun(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", OnProjectAttach: "echo attach-dry"},
		Windows: []config.Window{{Name: "w"}},
	}
	var transcript bytes.Buffer
	err := RunAttachHook(cfg, io.Discard, DryRun(&transcript))
	if err != nil {
		t.Fatalf("RunAttachHook: %v", err)
	}
	if !strings.Contains(transcript.String(), "# would run on_project_attach") {
		t.Errorf("transcript = %q, want # would run on_project_attach", transcript.String())
	}
}

// fakeHistory is a minimal in-memory HookHistory, standing in for
// *internal/state.Store without pulling in that package's file I/O.
type fakeHistory struct {
	started map[string]bool
	marked  []string // every dir MarkStarted was called with, in order
}

func (h *fakeHistory) Started(dir string) bool { return h.started[dir] }

func (h *fakeHistory) MarkStarted(dir string) error {
	h.marked = append(h.marked, dir)
	if h.started == nil {
		h.started = map[string]bool{}
	}
	h.started[dir] = true
	return nil
}

// loadConfig writes body to a temp .wyrm.toml and loads it through
// config.Load, so the resulting Config has Dir() populated — a config built
// as a struct literal (as most of this file's tests do) has no on-disk
// identity, but that's exactly the identity runFirstStartOrRestartHook keys
// on, so these tests need the real thing.
func loadConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/.wyrm.toml"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func TestCreateFirstStartFiresOnceThenRestart(t *testing.T) {
	body := "[session]\nname = \"proj\"\nroot = \".\"\n" +
		"on_project_first_start = \"echo first\"\non_project_restart = \"echo restart\"\n" +
		"[[windows]]\nname = \"w\"\n"
	cfg := loadConfig(t, body)
	hist := &fakeHistory{}

	r := &fakeRunner{}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr, WithHistory(hist)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(stderr.String(), "running on_project_first_start") {
		t.Errorf("stderr = %q, want on_project_first_start to run", stderr.String())
	}
	if strings.Contains(stderr.String(), "on_project_restart") {
		t.Errorf("stderr = %q, want on_project_restart NOT to run on a genuine first start", stderr.String())
	}
	if len(hist.marked) != 1 || hist.marked[0] != cfg.Dir() {
		t.Errorf("marked = %v, want exactly [%q]", hist.marked, cfg.Dir())
	}

	// Simulate a second build of the same project (e.g. after `wyrm kill`):
	// history now says it has started before, so restart fires instead.
	r2 := &fakeRunner{}
	var stderr2 bytes.Buffer
	if _, _, _, err := Create(r2, cfg, io.Discard, &stderr2, WithHistory(hist)); err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if !strings.Contains(stderr2.String(), "running on_project_restart") {
		t.Errorf("stderr = %q, want on_project_restart to run on a later start", stderr2.String())
	}
	if strings.Contains(stderr2.String(), "on_project_first_start") {
		t.Errorf("stderr = %q, want on_project_first_start NOT to run again", stderr2.String())
	}
	if len(hist.marked) != 1 {
		t.Errorf("marked = %v, want MarkStarted not called again", hist.marked)
	}
}

// TestCreateWildcardProjectsHaveIndependentLifecycleHistory is the
// regression test for keying lifecycle history off cfg.Dir(): every
// directory a [[wildcard]] pattern matches shares one template file (and so
// one cfg.Dir()), but Project.LoadConfig overrides each one's session.root
// to its own matched directory. History has to follow root, or only the
// first matched directory ever sees on_project_first_start — every other one
// is told (wrongly) that it has already started.
func TestCreateWildcardProjectsHaveIndependentLifecycleHistory(t *testing.T) {
	body := "[session]\nroot = \".\"\n" +
		"on_project_first_start = \"echo first\"\non_project_restart = \"echo restart\"\n" +
		"[[windows]]\nname = \"w\"\n"
	templatePath := t.TempDir() + "/.wyrm.toml"
	if err := os.WriteFile(templatePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Load the same template twice — like two directories matched by one
	// [[wildcard]] pattern — and override each one's root to its own matched
	// directory, exactly as Project.LoadConfig does. Both configs therefore
	// share cfg.Dir() (the template's own directory).
	cfgA, err := config.Load(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	cfgA.Session.Root = t.TempDir()

	cfgB, err := config.Load(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	cfgB.Session.Root = t.TempDir()

	if cfgA.Dir() != cfgB.Dir() {
		t.Fatalf("test setup: cfgA.Dir()=%q != cfgB.Dir()=%q, want them to collide like a wildcard template", cfgA.Dir(), cfgB.Dir())
	}

	hist := &fakeHistory{}

	var stderrA bytes.Buffer
	if _, _, _, err := Create(&fakeRunner{}, cfgA, io.Discard, &stderrA, WithHistory(hist)); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	if !strings.Contains(stderrA.String(), "running on_project_first_start") {
		t.Errorf("project A: stderr = %q, want on_project_first_start", stderrA.String())
	}

	var stderrB bytes.Buffer
	if _, _, _, err := Create(&fakeRunner{}, cfgB, io.Discard, &stderrB, WithHistory(hist)); err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if !strings.Contains(stderrB.String(), "running on_project_first_start") {
		t.Errorf("project B: stderr = %q, want its own on_project_first_start rather than being told it already started", stderrB.String())
	}
	if strings.Contains(stderrB.String(), "on_project_restart") {
		t.Errorf("project B: stderr = %q, must not run on_project_restart on its own genuine first start", stderrB.String())
	}
}

// TestCreateValidatesBeforeRunningHooks is the regression test for a
// malformed window root being discovered only after on_project_first_start
// had already run (possibly doing real, expensive setup work) and been
// recorded in history: fixing the config and retrying then fired
// on_project_restart instead, even though nothing had actually been built
// the first time.
func TestCreateValidatesBeforeRunningHooks(t *testing.T) {
	t.Setenv("WYRM_TEST_UNDEFINED_VAR_XYZ", "")
	os.Unsetenv("WYRM_TEST_UNDEFINED_VAR_XYZ")
	body := "[session]\nname = \"proj\"\nroot = \".\"\n" +
		"on_project_first_start = \"echo first\"\n" +
		"[[windows]]\nname = \"w\"\nroot = \"$WYRM_TEST_UNDEFINED_VAR_XYZ\"\n"
	cfg := loadConfig(t, body)
	hist := &fakeHistory{}

	r := &fakeRunner{}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr, WithHistory(hist)); err == nil {
		t.Fatal("Create = nil error, want the malformed window root to fail")
	}
	if strings.Contains(stderr.String(), "on_project_first_start") {
		t.Errorf("stderr = %q, on_project_first_start ran despite the config being invalid", stderr.String())
	}
	if len(hist.marked) != 0 {
		t.Errorf("marked = %v, want first-start not recorded for a build that never actually happened", hist.marked)
	}
}

func TestCreateNoHistoryNeitherHookFires(t *testing.T) {
	body := "[session]\nname = \"proj\"\nroot = \".\"\n" +
		"on_project_first_start = \"echo first\"\non_project_restart = \"echo restart\"\n" +
		"[[windows]]\nname = \"w\"\n"
	cfg := loadConfig(t, body)

	r := &fakeRunner{}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if strings.Contains(stderr.String(), "on_project_first_start") || strings.Contains(stderr.String(), "on_project_restart") {
		t.Errorf("stderr = %q, want neither hook without WithHistory", stderr.String())
	}
}

func TestCreateDryRunDoesNotMarkStarted(t *testing.T) {
	body := "[session]\nname = \"proj\"\nroot = \".\"\n" +
		"on_project_first_start = \"echo first\"\n[[windows]]\nname = \"w\"\n"
	cfg := loadConfig(t, body)
	hist := &fakeHistory{}

	var out bytes.Buffer
	dry := tmux.NewDryRun(&out)
	if _, _, _, err := Create(dry, cfg, io.Discard, io.Discard, DryRun(&out), WithHistory(hist)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.Contains(out.String(), "on_project_first_start") {
		t.Errorf("transcript = %q, want mention of on_project_first_start", out.String())
	}
	if len(hist.marked) != 0 {
		t.Errorf("marked = %v, want dry run not to record a start", hist.marked)
	}
}

func TestCreateNoOnDiskIdentitySkipsHooks(t *testing.T) {
	// A Config built as a literal (no config.Load) has Dir() == "" — the
	// built-in-default/in-memory case, which has no meaningful "has this
	// started before".
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj", Root: "/tmp/proj",
			OnProjectFirstStart: "echo first", OnProjectRestart: "echo restart",
		},
		Windows: []config.Window{{Name: "w"}},
	}
	hist := &fakeHistory{}
	r := &fakeRunner{}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr, WithHistory(hist)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if strings.Contains(stderr.String(), "on_project_first_start") || strings.Contains(stderr.String(), "on_project_restart") {
		t.Errorf("stderr = %q, want neither hook for a config with no on-disk identity", stderr.String())
	}
	if len(hist.marked) != 0 {
		t.Errorf("marked = %v, want nothing recorded", hist.marked)
	}
}

func TestEnablePaneTitles(t *testing.T) {
	on := true
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj", Root: "/tmp/proj",
			EnablePaneTitles: &on,
		},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var gotPosition, gotFormat string
	for _, c := range r.calls {
		if len(c) >= 5 && c[0] == "set-option" && c[3] == "pane-border-status" {
			gotPosition = c[4]
		}
		if len(c) >= 5 && c[0] == "set-option" && c[3] == "pane-border-format" {
			gotFormat = c[4]
		}
	}
	if gotPosition != "top" {
		t.Errorf("pane-border-status = %q, want default %q", gotPosition, "top")
	}
	if gotFormat != defaultPaneTitleFormat {
		t.Errorf("pane-border-format = %q, want default %q", gotFormat, defaultPaneTitleFormat)
	}
}

func TestEnablePaneTitlesCustomPositionAndFormat(t *testing.T) {
	on := true
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj", Root: "/tmp/proj",
			EnablePaneTitles:  &on,
			PaneTitlePosition: "bottom",
			PaneTitleFormat:   "#{pane_current_path}",
		},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var gotPosition, gotFormat string
	for _, c := range r.calls {
		if len(c) >= 5 && c[0] == "set-option" && c[3] == "pane-border-status" {
			gotPosition = c[4]
		}
		if len(c) >= 5 && c[0] == "set-option" && c[3] == "pane-border-format" {
			gotFormat = c[4]
		}
	}
	if gotPosition != "bottom" {
		t.Errorf("pane-border-status = %q, want %q", gotPosition, "bottom")
	}
	if gotFormat != "#{pane_current_path}" {
		t.Errorf("pane-border-format = %q, want %q", gotFormat, "#{pane_current_path}")
	}
}

func TestPaneTitlesOffByDefault(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "set-option" {
			t.Errorf("set-option called with pane titles unset: %v", c)
		}
	}
}

// TestCreatePostWindowRunsPerWindow covers both that post_window fires at
// all, and that each window's hook runs in its own resolved root — the
// thing that would break silently if roots[i] weren't threaded through the
// hook loop correctly.
func TestCreatePostWindowRunsPerWindow(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "marker")
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{
			{Name: "a", Root: dir1, PostWindow: "pwd >> " + marker},
			{Name: "b", Root: dir2, PostWindow: "pwd >> " + marker},
		},
	}
	r := &fakeRunner{}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr); err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("post_window never ran: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("marker has %d lines, want one per window: %q", len(lines), data)
	}
	want := []string{dir1, dir2}
	for i, w := range want {
		got, err := filepath.EvalSymlinks(lines[i])
		if err != nil {
			t.Fatalf("resolving %q: %v", lines[i], err)
		}
		resolvedWant, err := filepath.EvalSymlinks(w)
		if err != nil {
			t.Fatalf("resolving %q: %v", w, err)
		}
		if got != resolvedWant {
			t.Errorf("window %d ran post_window in %q, want %q", i, got, resolvedWant)
		}
	}
}

// TestCreatePostWindowFailureStillCreates matches the "cosmetic failure
// warns and continues" policy every other per-pane/per-hook failure in this
// package already follows.
func TestCreatePostWindowFailureStillCreates(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", PostWindow: "exit 1"}},
	}
	r := &fakeRunner{}
	var stderr bytes.Buffer
	name, _, created, err := Create(r, cfg, io.Discard, &stderr)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "proj" || !created {
		t.Errorf("name, created = %q, %v; want proj, true", name, created)
	}
	if !strings.Contains(stderr.String(), "post_window failed") {
		t.Errorf("stderr = %q, want post_window failure warning", stderr.String())
	}
}

func TestCreateNewSessionError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{fail: map[string]bool{"new-session": true}}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "creating session") {
		t.Errorf("Create error = %v, want containing %q", err, "creating session")
	}
}

func TestCreateNewWindowError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "first"}, {Name: "second"}},
	}
	r := &fakeRunner{fail: map[string]bool{"new-window": true}}
	_, _, _, err := Create(r, cfg, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `creating window "second"`) {
		t.Errorf("Create error = %v, want containing %q", err, `creating window "second"`)
	}
}

func TestCreateUnexpectedOutput(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{badNewSessionOutput: true}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unexpected tmux output") {
		t.Errorf("Create error = %v, want containing %q", err, "unexpected tmux output")
	}
}

func TestCreatePreWindowOnly(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", PreWindow: "echo hi"}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := r.joined()
	want := "send-keys -t %1 -l -- echo hi"
	count := 0
	for _, c := range got {
		if c == want {
			count++
		}
	}
	if count != 1 {
		t.Errorf("send-keys %q called %d times in:\n%s", want, count, strings.Join(got, "\n"))
	}
}

func TestApplySplitsSplitError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name: "w",
			Splits: []config.Split{
				{Type: "h", Command: "should-not-run"},
				{Command: "second-entry"},
			},
		}},
	}
	r := &fakeRunner{fail: map[string]bool{"split-window": true}}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := strings.Join(r.joined(), "\n")
	if strings.Contains(got, "should-not-run") {
		t.Errorf("command sent to a pane that failed to split:\n%s", got)
	}
	// The second, typeless entry reuses the base pane and must still run.
	if !strings.Contains(got, "second-entry") {
		t.Errorf("missing command for sibling entry after split failure:\n%s", got)
	}
}

func TestApplyPanesSplitError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name: "w",
			Panes: []config.Pane{
				{Command: "first"},
				{Command: "should-not-run"},
			},
		}},
	}
	r := &fakeRunner{fail: map[string]bool{"split-window": true}}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := strings.Join(r.joined(), "\n")
	if !strings.Contains(got, "send-keys -t %1 -l -- first") {
		t.Errorf("missing first pane command:\n%s", got)
	}
	if strings.Contains(got, "should-not-run") {
		t.Errorf("command sent to a pane that failed to split:\n%s", got)
	}
}

func TestApplyPanesSelectLayoutError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name:  "w",
			Panes: []config.Pane{{Command: "a"}, {Command: "b"}},
		}},
	}
	r := &fakeRunner{fail: map[string]bool{"select-layout": true}}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestSendKeysError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", Splits: []config.Split{{Command: "nvim"}}}},
	}
	r := &fakeRunner{fail: map[string]bool{"send-keys": true}}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestSelectStartupWindowNotFound(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", StartupWindow: "missing"},
		Windows: []config.Window{{Name: "w"}},
	}
	// The live window list has "w" but not "missing", so no select-window fires.
	r := &fakeRunner{listWindowsOutput: "0|@1|1|layout|w"}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := strings.Join(r.joined(), "\n")
	if strings.Contains(got, "select-window") {
		t.Errorf("select-window called for a startup_window that isn't in the window list:\n%s", got)
	}
}

func TestSelectStartupSelectWindowError(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", StartupWindow: "w"},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{
		listWindowsOutput: "0|@1|1|layout|w",
		fail:              map[string]bool{"select-window": true},
	}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := strings.Join(r.joined(), "\n")
	if strings.Contains(got, "select-pane") {
		t.Errorf("select-pane called after select-window failure:\n%s", got)
	}
}

func TestSelectStartupSelectPaneError(t *testing.T) {
	pane := 0
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj", StartupWindow: "w", StartupPane: &pane},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{
		listWindowsOutput: "0|@1|1|layout|w",
		listPanesOutput:   map[string]string{"@1": "%1|0|1|zsh"},
		fail:              map[string]bool{"select-pane": true},
	}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// TestCreateWindowAndSplitRoots pins the exact -c arguments: a window root is
// relative to the session root, a split root to its window's, and an absolute
// one escapes both.
func TestCreateWindowAndSplitRoots(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{
			{Name: "api", Root: "api", Splits: []config.Split{
				{},
				{Type: "h", Root: "deep"},
				{Type: "v", Root: "/elsewhere"},
			}},
			{Name: "plain", Splits: []config.Split{{}}},
		},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	joined := strings.Join(r.joined(), "\n")
	for _, want := range []string{
		"new-session -d -P -F #{session_id}|#{window_id}|#{pane_id} -s proj -n api -c /tmp/proj/api",
		"split-window -d -t %1 -h -P -F #{pane_id} -c /tmp/proj/api/deep",
		"split-window -d -t %2 -v -P -F #{pane_id} -c /elsewhere",
		"new-window -d -P -F #{window_id}|#{pane_id} -t $1 -n plain -c /tmp/proj",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in:\n%s", want, joined)
		}
	}
}

// TestCreateRunStartsProcessAndSkipsSendKeys: `run` hands the command to tmux as
// the pane's process, so nothing is typed into a shell that isn't there.
func TestCreateRunStartsProcessAndSkipsSendKeys(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name: "w",
			Splits: []config.Split{
				{Run: "npm run dev"},
				{Type: "h", Run: "-flaglike"},
				{Type: "v", Command: "typed"},
			},
		}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	joined := strings.Join(r.joined(), "\n")
	// The window's initial pane gets its process from new-session itself.
	if !strings.Contains(joined, "respawn-pane -k -t %1 -c /tmp/proj -- npm run dev") {
		t.Errorf("initial pane did not start its run command:\n%s", joined)
	}
	// "--" guards a command that looks like a flag.
	if !strings.Contains(joined, "-- -flaglike") {
		t.Errorf("a dash-leading run command was not guarded by --:\n%s", joined)
	}
	// Only the `command` entry is typed.
	if n := strings.Count(joined, "send-keys"); n != 2 {
		t.Errorf("got %d send-keys calls, want 2 (one -l plus its Enter) — run must not type:\n%s", n, joined)
	}
	if !strings.Contains(joined, "send-keys -t %3 -l -- typed") {
		t.Errorf("the command entry was not typed:\n%s", joined)
	}
}

// TestCreatePreWindowSkipsDirectRunPanes is the regression test for
// pre_window being typed into a pane with no shell: a split's `run` starts
// its own process directly (see paneProcess/splitPane), so pre_window landing
// there is literal input to that process rather than shell setup. This
// covers both the window's initial pane (splits[0] with no type) and a
// later, nested one.
func TestCreatePreWindowSkipsDirectRunPanes(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name:      "w",
			PreWindow: "nvm use 18",
			Splits: []config.Split{
				{Run: "cat"},
				{Type: "h", Run: "less"},
				{Type: "v", Command: "typed"},
			},
		}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	joined := strings.Join(r.joined(), "\n")
	if strings.Contains(joined, "send-keys -t %1") || strings.Contains(joined, "send-keys -t %2") {
		t.Errorf("pre_window was typed into a direct-run pane's process:\n%s", joined)
	}
	// The one shell pane (the "typed" split) still gets it.
	if !strings.Contains(joined, "send-keys -t %3 -l -- nvm use 18") {
		t.Errorf("pre_window was not sent to the one pane with a shell:\n%s", joined)
	}
}

// TestCreateWindowRootDoesNotOverrideSplitRoot is the regression test for the
// initial split's own root being ignored whenever the window also set one:
// Split.Root is documented to override the window's directory for its pane
// regardless, but the precedence check used to require window.root to be
// empty first.
func TestCreateWindowRootDoesNotOverrideSplitRoot(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{
			Name: "w",
			Root: "backend",
			Splits: []config.Split{
				{Root: "deep", Command: "typed"},
			},
		}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	joined := strings.Join(r.joined(), "\n")
	if !strings.Contains(joined, "-c /tmp/proj/backend/deep") {
		t.Errorf("initial pane did not honor the split's own root over the window's:\n%s", joined)
	}
}

// TestCreatePassesEnvToEveryPaneCommand: tmux's set-environment only reaches
// processes started afterward, so the vars have to ride along on each
// new-session/new-window/split-window instead.
func TestCreatePassesEnv(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj", Root: "/tmp/proj",
			Env: map[string]string{"NODE_ENV": "development", "API_URL": "http://x"},
		},
		Windows: []config.Window{{Name: "w", Splits: []config.Split{{}, {Type: "h"}}}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, c := range r.joined() {
		switch {
		case strings.HasPrefix(c, "new-session"), strings.HasPrefix(c, "split-window"):
			// Sorted, so a build is reproducible and a dry run is stable.
			if !strings.Contains(c, "-e API_URL=http://x -e NODE_ENV=development") {
				t.Errorf("call %q is missing the sorted env args", c)
			}
		}
	}
}

func TestCreateCascadesEnv(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj",
			Root: "/tmp/proj",
			Env: map[string]string{
				"GLOBAL":   "1",
				"OVERRIDE": "session",
				"PORT":     "3000",
			},
		},
		Windows: []config.Window{
			{
				Name: "api",
				Env: map[string]string{
					"OVERRIDE": "window",
					"API_KEY":  "secret",
				},
				Splits: []config.Split{
					{
						Env:     map[string]string{"OVERRIDE": "split0"},
						Command: "npm run dev",
					},
					{
						Type:    "h",
						Env:     map[string]string{"OVERRIDE": "split1", "SUB": "child"},
						Command: "npm test",
					},
				},
			},
			{
				Name: "web",
				Splits: []config.Split{
					{
						Env:     map[string]string{"PORT": "4000"},
						Command: "python app.py",
					},
				},
			},
		},
	}

	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}

	calls := r.joined()
	// Check new-session (window 0 initial pane)
	newSessionCall := ""
	for _, c := range calls {
		if strings.HasPrefix(c, "new-session") {
			newSessionCall = c
			break
		}
	}
	if newSessionCall == "" || !strings.Contains(newSessionCall, "-e API_KEY=secret -e GLOBAL=1 -e OVERRIDE=split0 -e PORT=3000") {
		t.Errorf("new-session call %q missing cascaded env", newSessionCall)
	}

	// Check split-window (window 0 split 1)
	splitCall := ""
	for _, c := range calls {
		if strings.HasPrefix(c, "split-window") {
			splitCall = c
			break
		}
	}
	if splitCall == "" || !strings.Contains(splitCall, "-e API_KEY=secret -e GLOBAL=1 -e OVERRIDE=split1 -e PORT=3000 -e SUB=child") {
		t.Errorf("split-window call %q missing cascaded env", splitCall)
	}

	// Check new-window (window 1 initial pane)
	newWinCall := ""
	for _, c := range calls {
		if strings.HasPrefix(c, "new-window") {
			newWinCall = c
			break
		}
	}
	if newWinCall == "" || !strings.Contains(newWinCall, "-e GLOBAL=1 -e OVERRIDE=session -e PORT=4000") {
		t.Errorf("new-window call %q missing cascaded env", newWinCall)
	}
}

// batchingRunner is a fakeRunner that also implements tmux.BatchRunner, so a
// test can see that the build actually batches rather than quietly falling back
// to one call per command — which is what every other mock here does.
type batchingRunner struct {
	fakeRunner
	batches [][][]string
	// direct records only the calls issued one at a time, so a test can tell a
	// batched command from one that bypassed the batch. RunBatch reuses
	// fakeRunner.Run to fabricate outputs, which would otherwise make the two
	// indistinguishable.
	direct  []string
	inBatch bool
}

func (b *batchingRunner) Run(args ...string) (string, error) {
	if !b.inBatch {
		b.direct = append(b.direct, strings.Join(args, " "))
	}
	return b.fakeRunner.Run(args...)
}

func (b *batchingRunner) RunBatch(cmds [][]string) ([]string, error) {
	b.batches = append(b.batches, cmds)
	b.inBatch = true
	defer func() { b.inBatch = false }()
	outs := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out, err := b.Run(c...)
		if err != nil {
			return outs, err
		}
		outs = append(outs, out)
	}
	return outs, nil
}

// TestCreateBatchesEveryCommandIntoOneCall is the point of the whole exercise:
// a six-pane build issued twelve send-keys processes, and now issues one batch.
func TestCreateBatchesEveryCommandIntoOneCall(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{
			{Name: "a", Splits: []config.Split{
				{Command: "one"}, {Type: "h", Command: "two"}, {Type: "v", Command: "three"},
			}},
			{Name: "b", Splits: []config.Split{{Command: "four"}, {Type: "h", Command: "five"}}},
			{Name: "c", Splits: []config.Split{{Command: "six"}}},
		},
	}

	r := &batchingRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(r.batches) != 1 {
		t.Fatalf("issued %d batches, want exactly 1", len(r.batches))
	}
	// Six commands, each a literal plus an Enter.
	if got := len(r.batches[0]); got != 12 {
		t.Errorf("batch held %d commands, want 12", got)
	}
	for _, c := range r.batches[0] {
		if c[0] != "send-keys" {
			t.Errorf("batch contained a non-send-keys command %v", c)
		}
	}
	// And nothing was typed outside the batch.
	for _, c := range r.direct {
		if strings.HasPrefix(c, "send-keys") {
			t.Errorf("send-keys issued outside the batch: %q", c)
		}
	}
}

// A pane that dies mid-build must not cancel the commands queued after it, and
// must not cause an already-typed command to be typed a second time.
func TestCreateReplaysOnlyAfterAFailedSend(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{{Name: "w", Splits: []config.Split{
			{Command: "first"}, {Type: "h", Command: "second"}, {Type: "v", Command: "third"},
		}}},
	}

	// Fail the send to %2 — the middle pane — however it is issued.
	r := &partialBatchRunner{failTarget: "%2"}
	var stderr bytes.Buffer
	if _, _, _, err := Create(r, cfg, io.Discard, &stderr); err != nil {
		t.Fatalf("Create: %v", err)
	}

	typed := r.typed()
	// %1 and %3 still got their commands: one bad pane doesn't cancel the rest.
	for _, want := range []string{"first", "third"} {
		if !slices.Contains(typed, want) {
			t.Errorf("%q was never typed; typed=%v", want, typed)
		}
	}
	// Nothing was typed twice — replaying a send-keys would duplicate input.
	seen := map[string]int{}
	for _, c := range typed {
		seen[c]++
	}
	for c, n := range seen {
		if n > 1 {
			t.Errorf("%q was typed %d times, want once", c, n)
		}
	}
	if !strings.Contains(stderr.String(), "second") {
		t.Errorf("stderr = %q, want a warning naming the command that failed", stderr.String())
	}
}

// partialBatchRunner batches, but fails any send-keys aimed at failTarget —
// which stops the batch there, exactly as tmux does.
type partialBatchRunner struct {
	fakeRunner
	failTarget string
	sent       []string
}

func (p *partialBatchRunner) send(args []string) (string, error) {
	if args[0] == "send-keys" && args[2] == p.failTarget {
		return "can't find pane", errors.New("exit status 1")
	}
	// Record only the literal half, which carries the command text.
	if args[0] == "send-keys" && len(args) > 5 && args[3] == "-l" {
		p.sent = append(p.sent, args[5])
	}
	return p.fakeRunner.Run(args...)
}

func (p *partialBatchRunner) Run(args ...string) (string, error) { return p.send(args) }

func (p *partialBatchRunner) RunBatch(cmds [][]string) ([]string, error) {
	outs := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out, err := p.send(c)
		if err != nil {
			return outs, err // tmux abandons the rest of the batch
		}
		outs = append(outs, out)
	}
	return outs, nil
}

func (p *partialBatchRunner) typed() []string { return p.sent }

// TestCreateRestoresSynchronizePanes covers how synchronize-panes is put back
// after startup typing, which runs with it off. A window with no value of its
// own must be unset rather than given an explicit copy of the global value, or
// later changes to the global setting would no longer reach it.
func TestCreateRestoresSynchronizePanes(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name    string
		own     string // the window's own value before startup
		setting *bool  // the config's synchronize
		want    string
	}{
		{"inherited", "", nil, "set-window-option -u -t @1 synchronize-panes"},
		{"own value kept", "on", nil, "set-window-option -t @1 synchronize-panes on"},
		{"config on", "", &on, "set-window-option -t @1 synchronize-panes on"},
		{"config off", "on", &off, "set-window-option -t @1 synchronize-panes off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Session: config.Session{Name: "proj", Root: "/tmp/proj"},
				Windows: []config.Window{{Name: "w", Synchronize: tt.setting, Splits: []config.Split{{Command: "ls"}}}},
			}
			r := &fakeRunner{windowSync: tt.own}
			var stdout, stderr bytes.Buffer
			if _, _, _, err := Create(r, cfg, &stdout, &stderr); err != nil {
				t.Fatalf("Create: %v", err)
			}
			got := r.joined()
			typed := -1
			for i, c := range got {
				if c == "send-keys -t %1 Enter" {
					typed = i
				}
			}
			var restores []int
			for i, c := range got {
				if strings.Contains(c, "synchronize-panes") && i > typed {
					restores = append(restores, i)
				}
			}
			if typed < 0 || len(restores) != 1 || got[restores[0]] != tt.want {
				t.Fatalf("want exactly %q after startup typing, calls:\n%s", tt.want, strings.Join(got, "\n"))
			}
		})
	}
}

func TestCreateSynchronizeRemainOnExitZoomed(t *testing.T) {
	tTrue := true
	cfg := &config.Config{
		Session: config.Session{Name: "proj", Root: "/tmp/proj"},
		Windows: []config.Window{
			{
				Name:         "win1",
				Synchronize:  &tTrue,
				RemainOnExit: &tTrue,
				Splits: []config.Split{
					{Command: "echo pane1", RemainOnExit: &tTrue},
					{Type: "h", Command: "echo pane2", Zoomed: &tTrue},
				},
			},
		},
	}

	r := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	name, id, created, err := Create(r, cfg, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if name != "proj" || id != "$1" || !created {
		t.Fatalf("Create returned name=%q, id=%q, created=%v", name, id, created)
	}

	joined := r.joined()
	foundSync := false
	foundWinRemain := false
	foundPaneRemain := false
	foundZoom := false

	for _, call := range joined {
		if strings.Contains(call, "set-window-option -t @1 synchronize-panes on") {
			foundSync = true
		}
		if strings.Contains(call, "set-window-option -t @1 remain-on-exit on") {
			foundWinRemain = true
		}
		if strings.Contains(call, "set-option -p -t %1 remain-on-exit on") {
			foundPaneRemain = true
		}
		if strings.Contains(call, "resize-pane -Z -t %2") {
			foundZoom = true
		}
	}

	if !foundSync {
		t.Errorf("expected set-window-option synchronize-panes on, calls: %v", joined)
	}
	if !foundWinRemain {
		t.Errorf("expected set-window-option remain-on-exit on, calls: %v", joined)
	}
	if !foundPaneRemain {
		t.Errorf("expected set-option -p remain-on-exit on for %%1, calls: %v", joined)
	}
	if !foundZoom {
		t.Errorf("expected resize-pane -Z for %%2, calls: %v", joined)
	}
}

// tmux's run-shell expands its argument as a FORMAT whatever the quoting, so
// an on_project_detach body containing "#{...}" was rewritten before the shell
// saw it and "#(...)" was executed by tmux at expansion time. "##" is the
// documented literal "#", and the surrounding single quotes keep tmux's
// command lexer out of the body. Verified against tmux 3.7.
func TestDetachHookCommandEscapesTmuxSyntax(t *testing.T) {
	for _, tc := range []struct {
		name, hook, want string
	}{
		{"plain", "echo bye", `run-shell 'echo bye'`},
		{"format specifier", "echo #{session_name}", `run-shell 'echo ##{session_name}'`},
		{"command substitution", "echo #(id -un)", `run-shell 'echo ##(id -un)'`},
		{"literal hash", "echo '# comment'", `run-shell 'echo '\''## comment'\'''`},
		{"single quote", "echo it's", `run-shell 'echo it'\''s'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := detachHookCommand(tc.hook); got != tc.want {
				t.Errorf("detachHookCommand(%q)\n got %s\nwant %s", tc.hook, got, tc.want)
			}
		})
	}
}

func TestCreateDetachHookIsEscaped(t *testing.T) {
	cfg := &config.Config{
		Session: config.Session{
			Name: "proj", Root: "/tmp/proj",
			OnProjectDetach: "echo #{session_name}",
		},
		Windows: []config.Window{{Name: "w"}},
	}
	r := &fakeRunner{}
	if _, _, _, err := Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, call := range r.calls {
		if len(call) >= 5 && call[0] == "set-hook" && call[3] == "client-detached" {
			if !strings.Contains(call[4], "##{session_name}") {
				t.Errorf("hook argument = %q, want the # doubled so tmux does not expand it", call[4])
			}
			return
		}
	}
	t.Error("no set-hook client-detached call")
}
