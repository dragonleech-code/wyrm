package session_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/freeze"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/tmux"
)

// TestIntegrationNestedSplitGeometry drives a real tmux server and checks the
// *shape* of the result, not just the pane count.
//
// The layout below is the case applySplits used to get wrong: a nested
// container that is not the last sibling. Children were created before the
// next sibling, so that sibling split a pane its own children had already
// subdivided — the left column ended up 24 rows tall instead of the full
// window, and the right-hand pane took the wrong share. Siblings are now
// created breadth-first, so `size` means "share of this container" and a
// layout captured by `wyrm save` rebuilds to the geometry it came from.
func TestIntegrationNestedSplitGeometry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-geom-%d", os.Getpid())}
	t.Cleanup(func() { r.Run("kill-server") }) //nolint:errcheck

	root := t.TempDir()
	cfg := &config.Config{
		Session: config.Session{Name: "geom", Root: root},
		Windows: []config.Window{{
			Name: "w",
			Splits: []config.Split{
				// Left column, itself split top/bottom.
				{Command: "# left-top", Children: []config.Split{
					{Command: "# still-left-top"},
					{Type: "v", Size: 50, Command: "# left-bottom"},
				}},
				// Right column: 30% of the *window*, full height.
				{Type: "h", Size: 30, Command: "# right"},
			},
		}},
	}

	_, sessionID, _, err := session.Create(r, cfg, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	windows, err := tmux.ListWindows(r, sessionID)
	if err != nil || len(windows) != 1 {
		t.Fatalf("ListWindows = %v, %v; want one window", windows, err)
	}
	panes := paneGeometry(t, r, windows[0].ID)
	if len(panes) != 3 {
		t.Fatalf("got %d panes, want 3: %+v", len(panes), panes)
	}

	winW, winH := windowSize(t, r, windows[0].ID)

	// The right-hand pane must span the window's full height. Under the old
	// evaluation order it only spanned the top half of the left column's split.
	var right *paneGeom
	for i := range panes {
		if panes[i].x > 0 {
			right = &panes[i]
		}
	}
	if right == nil {
		t.Fatalf("no right-hand pane found in %+v", panes)
	}
	if right.h != winH {
		t.Errorf("right pane height = %d, want the full window height %d (it was carved out of an already-split column)", right.h, winH)
	}
	// 30% of the window width, allowing for tmux's divider and rounding.
	if want := winW * 30 / 100; abs(right.w-want) > 2 {
		t.Errorf("right pane width = %d, want ~%d (30%% of %d)", right.w, want, winW)
	}

	// And the round trip: freezing the live layout and rebuilding it must
	// produce the same geometry.
	frozen, err := freeze.Config(r, sessionID, "geom2", root)
	if err != nil {
		t.Fatalf("freeze.Config: %v", err)
	}
	frozen.Session.Name = "geom2"
	if _, secondID, _, err := session.Create(r, frozen, os.Stdout, os.Stderr); err != nil {
		t.Fatalf("Create (rebuilt): %v", err)
	} else {
		rebuiltWindows, err := tmux.ListWindows(r, secondID)
		if err != nil || len(rebuiltWindows) == 0 {
			t.Fatalf("ListWindows (rebuilt): %v, %v", rebuiltWindows, err)
		}
		rebuilt := paneGeometry(t, r, rebuiltWindows[0].ID)
		if len(rebuilt) != len(panes) {
			t.Fatalf("rebuilt has %d panes, want %d", len(rebuilt), len(panes))
		}
		for i := range panes {
			if abs(rebuilt[i].w-panes[i].w) > 2 || abs(rebuilt[i].h-panes[i].h) > 2 {
				t.Errorf("pane %d rebuilt as %dx%d at (%d,%d), want ~%dx%d at (%d,%d)",
					i, rebuilt[i].w, rebuilt[i].h, rebuilt[i].x, rebuilt[i].y,
					panes[i].w, panes[i].h, panes[i].x, panes[i].y)
			}
		}
	}
}

type paneGeom struct{ w, h, x, y int }

// paneGeometry reads every pane's size and position, ordered by tmux's own
// pane index so two windows can be compared entry by entry.
func paneGeometry(t *testing.T, r tmux.Runner, windowID string) []paneGeom {
	t.Helper()
	out, err := r.Run("list-panes", "-t", windowID, "-F", "#{pane_width}|#{pane_height}|#{pane_left}|#{pane_top}")
	if err != nil {
		t.Fatalf("list-panes: %v (%s)", err, out)
	}
	var panes []paneGeom
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) != 4 {
			t.Fatalf("unexpected list-panes line %q", line)
		}
		panes = append(panes, paneGeom{atoi(t, f[0]), atoi(t, f[1]), atoi(t, f[2]), atoi(t, f[3])})
	}
	return panes
}

func windowSize(t *testing.T, r tmux.Runner, windowID string) (int, int) {
	t.Helper()
	out, err := r.Run("display-message", "-p", "-t", windowID, "-F", "#{window_width}|#{window_height}")
	if err != nil {
		t.Fatalf("display-message: %v (%s)", err, out)
	}
	f := strings.Split(strings.TrimSpace(out), "|")
	if len(f) != 2 {
		t.Fatalf("unexpected window size %q", out)
	}
	return atoi(t, f[0]), atoi(t, f[1])
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("not a number: %q", s)
	}
	return n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// TestIntegrationSplitPanesUseSessionRoot is the regression guard for a bug that
// only shows itself when wyrm is run from somewhere other than the project:
// split-window without an explicit -c starts the pane in the *invoking client's*
// working directory, not the session's. So `wyrm api` from ~ built a session
// rooted at the project whose every split pane sat in ~ — and only the window's
// initial pane, created by new-session/new-window -c, was ever right.
//
// It needs a real tmux because the whole bug is in what tmux does with a missing
// flag; a mock runner can only assert the flag is passed.
func TestIntegrationSplitPanesUseSessionRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	root := t.TempDir()
	// Stand somewhere else entirely, which is what makes the bug visible.
	t.Chdir(t.TempDir())

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-root-it-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	cfg := &config.Config{
		Session: config.Session{Name: "rootit", Root: root},
		Windows: []config.Window{{
			Name: "w",
			Splits: []config.Split{
				{},
				{Type: "h"},
				{Type: "v"},
			},
		}},
	}
	if _, _, _, err := session.Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// macOS hands out /var symlinks for temp dirs; compare resolved paths.
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving root: %v", err)
	}
	paths := panePaths(t, r, "rootit", "#{pane_id}")
	if len(paths) != 3 {
		t.Fatalf("got %d panes, want 3: %v", len(paths), paths)
	}
	for id, got := range paths {
		if got[0] != wantRoot {
			t.Errorf("pane %s is in %q, want the session root %q", id, got[0], wantRoot)
		}
	}
}

// panePaths reads every pane's resolved working directory for a session, keyed
// however keyFormat names them.
//
// It retries while any path comes back empty. tmux learns a pane's cwd from the
// process it started, so immediately after a build it may not have one yet —
// and the faster the build, the likelier that is. Asserting on the empty string
// produced a confusing "pane is in \".\"" failure roughly one run in twenty,
// because filepath.EvalSymlinks("") is ".".
func panePaths(t *testing.T, r tmux.Runner, session, keyFormat string) map[string][]string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := r.Run("list-panes", "-s", "-t", session, "-F", keyFormat+"|#{pane_current_path}")
		if err != nil {
			t.Fatalf("list-panes: %v (%s)", err, out)
		}
		byKey := map[string][]string{}
		empty := false
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			key, path, ok := strings.Cut(line, "|")
			if !ok {
				t.Fatalf("unexpected list-panes line %q", line)
			}
			if path == "" {
				empty = true
				break
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatalf("resolving pane path %q: %v", path, err)
			}
			byKey[key] = append(byKey[key], resolved)
		}
		if !empty {
			return byKey
		}
		if time.Now().After(deadline) {
			t.Fatalf("panes still reported no working directory after 3s:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestIntegrationWindowAndSplitRoots covers the per-window and per-split root
// keys, including that a relative one resolves against its parent rather than
// against the process's working directory.
func TestIntegrationWindowAndSplitRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	root := t.TempDir()
	for _, sub := range []string{"api", "api/deep", "web"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-wroot-it-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	cfg := &config.Config{
		Session: config.Session{Name: "wrootit", Root: root},
		Windows: []config.Window{
			// Window 0 sets its own root: new-session's -c has to follow it.
			{Name: "api", Root: "api", Splits: []config.Split{
				{},
				{Type: "h", Root: "deep"}, // relative to the window's root
			}},
			{Name: "web", Root: "web", Splits: []config.Split{{}}},
			{Name: "plain", Splits: []config.Split{{}}}, // inherits the session root
		},
	}
	if _, _, _, err := session.Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Grouped by window in tmux's own order rather than keyed by pane_index:
	// this machine's pane-base-index is 1, and hardcoding 0 would make the test
	// fail on exactly the setting wyrm targets panes by ID to be immune to.
	resolved := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("resolving %q: %v", p, err)
		}
		return r
	}
	got := panePaths(t, r, "wrootit", "#{window_name}")
	want := map[string][]string{
		"api":   {resolved(filepath.Join(root, "api")), resolved(filepath.Join(root, "api", "deep"))},
		"web":   {resolved(filepath.Join(root, "web"))},
		"plain": {resolved(root)},
	}
	if len(got) != len(want) {
		t.Fatalf("got windows %v, want %d of them", got, len(want))
	}
	for win, w := range want {
		if !reflect.DeepEqual(got[win], w) {
			t.Errorf("window %q pane dirs = %v, want %v", win, got[win], w)
		}
	}
}

// TestIntegrationRunStartsProcessDirectly: `run` makes the command the pane's
// own process rather than typing it into a shell, which is the whole difference
// — pane_current_command reports the program, not the shell hosting it.
func TestIntegrationRunStartsProcessDirectly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	r := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-run-it-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = r.Run("kill-server") })

	cfg := &config.Config{
		Session: config.Session{Name: "runit", Root: t.TempDir()},
		Windows: []config.Window{{
			Name: "w",
			Splits: []config.Split{
				{Run: "sleep 60"},            // the window's initial pane
				{Type: "h", Run: "sleep 61"}, // a split
			},
		}},
	}
	if _, _, _, err := session.Create(r, cfg, io.Discard, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// tmux needs a moment to report the newly exec'd process.
	var out string
	for i := 0; i < 20; i++ {
		var err error
		out, err = r.Run("list-panes", "-t", "runit", "-a", "-F", "#{pane_current_command}")
		if err != nil {
			t.Fatalf("list-panes: %v (%s)", err, out)
		}
		if strings.Count(out, "sleep") == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("pane commands = %q, want both panes running sleep directly", out)
}

// TestIntegrationBatchedBuildSpawnsFewerProcesses measures the thing the
// batching exists for: how many times wyrm forks tmux to build a session.
//
// It counts by wrapping the real Exec rather than trusting a mock, because the
// whole optimization lives in Exec.RunBatch — a mock that doesn't implement
// BatchRunner would pass this while proving nothing.
func TestIntegrationBatchedBuildSpawnsFewerProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	root := t.TempDir()
	cfg := func(name string) *config.Config {
		return &config.Config{
			Session: config.Session{Name: name, Root: root},
			Windows: []config.Window{
				{Name: "a", Splits: []config.Split{
					{Command: "echo one"}, {Type: "h", Command: "echo two"}, {Type: "v", Command: "echo three"},
				}},
				{Name: "b", Splits: []config.Split{{Command: "echo four"}, {Type: "h", Command: "echo five"}}},
				{Name: "c", Splits: []config.Split{{Command: "echo six"}}},
			},
		}
	}

	base := tmux.Exec{ConfigFile: os.DevNull, SocketName: fmt.Sprintf("wyrm-spawn-it-%d", os.Getpid())}
	t.Cleanup(func() { _, _ = base.Run("kill-server") })

	batched := &spawnCounter{Exec: base}
	if _, _, _, err := session.Create(batched, cfg("spawnbatched"), io.Discard, io.Discard); err != nil {
		t.Fatalf("Create (batched): %v", err)
	}

	// The same build through a Runner that cannot batch, for the comparison.
	unbatched := &noBatchCounter{exec: base}
	if _, _, _, err := session.Create(unbatched, cfg("spawnplain"), io.Discard, io.Discard); err != nil {
		t.Fatalf("Create (unbatched): %v", err)
	}

	t.Logf("processes: batched=%d unbatched=%d", batched.n, unbatched.n)
	if batched.n >= unbatched.n {
		t.Errorf("batched build spawned %d processes, unbatched %d — batching saved nothing",
			batched.n, unbatched.n)
	}
	// Twelve send-keys collapse into one, so the saving is at least eleven.
	if saved := unbatched.n - batched.n; saved < 11 {
		t.Errorf("batching saved only %d processes, want at least 11", saved)
	}

	// And both sessions must have come out identical.
	for _, name := range []string{"spawnbatched", "spawnplain"} {
		out, err := base.Run("list-panes", "-s", "-t", name, "-F", "#{window_name}|#{pane_id}")
		if err != nil {
			t.Fatalf("list-panes %s: %v (%s)", name, err, out)
		}
		if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 6 {
			t.Errorf("session %s has %d panes, want 6", name, n)
		}
	}
}

// spawnCounter counts tmux processes, batching included.
type spawnCounter struct {
	tmux.Exec
	n int
}

func (s *spawnCounter) Run(args ...string) (string, error) {
	s.n++
	return s.Exec.Run(args...)
}

func (s *spawnCounter) RunBatch(cmds [][]string) ([]string, error) {
	s.n++
	return s.Exec.RunBatch(cmds)
}

// noBatchCounter deliberately does not implement tmux.BatchRunner, so RunEach
// falls back to one process per command.
//
// It holds Exec in a named field rather than embedding it: embedding would
// *promote* Exec.RunBatch onto this type, making it satisfy BatchRunner after
// all — and the batch would then run through Exec directly, uncounted. The
// first version of this test did exactly that and reported the unbatched build
// as the cheaper one.
type noBatchCounter struct {
	exec tmux.Exec
	n    int
}

func (s *noBatchCounter) Run(args ...string) (string, error) {
	s.n++
	return s.exec.Run(args...)
}
