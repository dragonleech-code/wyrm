package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// funcRunner dispatches tmux calls to a function so tests can stub per-command
// output. The first arg (the tmux subcommand) is the usual discriminator.
type funcRunner struct {
	fn func(args ...string) (string, error)
}

func (r funcRunner) Run(args ...string) (string, error) { return r.fn(args...) }

func nopRunner() funcRunner {
	return funcRunner{fn: func(_ ...string) (string, error) { return "", nil }}
}

// run executes a command (if non-nil) and returns its message.
func run(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func TestFocusCycling(t *testing.T) {
	m := New(nopRunner(), nil)
	if m.focus != panelSessions {
		t.Fatalf("initial focus = %d, want panelSessions", m.focus)
	}
	m, _ = update(m, key("tab"))
	if m.focus != panelWindows {
		t.Errorf("after tab focus = %d, want panelWindows", m.focus)
	}
	m, _ = update(m, key("shift+tab"))
	if m.focus != panelSessions {
		t.Errorf("after shift+tab focus = %d, want panelSessions", m.focus)
	}
	m, _ = update(m, key("4"))
	if m.focus != panelPanes {
		t.Errorf("after '4' focus = %d, want panelPanes", m.focus)
	}
	// tab wraps from the last panel back to the first.
	m, _ = update(m, key("tab"))
	if m.focus != panelProjects {
		t.Errorf("after wrap focus = %d, want panelProjects", m.focus)
	}
}

// The default focus must leave the preview on the live pane capture: an
// initial projects load has to stay silent rather than pull the main panel
// over to a config file.
func TestInitialFocusKeepsPanePreview(t *testing.T) {
	m := New(nopRunner(), nil)
	m, cmd := update(m, projectsMsg{projects: []Project{{Name: "webapp", Path: "/tmp/.wyrm.toml"}}})
	if m.previewSrc != previewPane {
		t.Errorf("previewSrc = %d, want previewPane", m.previewSrc)
	}
	if cmd != nil {
		t.Errorf("projects load with Sessions focused produced %T, want no command", run(cmd))
	}
}

func TestSessionsMsgLoadsWindows(t *testing.T) {
	r := funcRunner{fn: func(args ...string) (string, error) {
		if args[0] == "list-windows" {
			return "0|@1|1|layout|code", nil
		}
		return "", nil
	}}
	m := New(r, nil)
	m, cmd := update(m, sessionsMsg{sessions: []sessions.Session{{ID: "$1", Name: "alpha", Windows: 1}}})
	if len(m.sessions) != 1 || m.cur[panelSessions] != 0 {
		t.Fatalf("sessions not stored: %+v cur=%d", m.sessions, m.cur[panelSessions])
	}
	msg := run(cmd)
	wm, ok := msg.(windowsMsg)
	if !ok {
		t.Fatalf("follow-up msg = %T, want windowsMsg", msg)
	}
	if wm.sessionID != "$1" {
		t.Errorf("windowsMsg.sessionID = %q, want $1", wm.sessionID)
	}
}

// TestSessionsMsgPreservesSelectionAcrossReorder is the regression test for a
// periodic refresh silently retargeting the selection: m.cur[panelSessions]
// is a plain index, so replacing m.sessions outright with a differently
// ordered list (a new session appearing ahead of the selected one, tmux
// reordering by activity, etc.) used to leave the same index number pointing
// at whichever session ended up in that slot, with no input from the user.
func TestSessionsMsgPreservesSelectionAcrossReorder(t *testing.T) {
	m := New(nopRunner(), nil)
	m.sessions = []sessions.Session{
		{ID: "$1", Name: "alpha"},
		{ID: "$2", Name: "beta"},
	}
	m.cur[panelSessions] = 1 // beta selected

	// A refresh reports a new session ahead of beta, shifting its index from
	// 1 to 2 with no action from the user.
	m, _ = update(m, sessionsMsg{sessions: []sessions.Session{
		{ID: "$3", Name: "new-session"},
		{ID: "$1", Name: "alpha"},
		{ID: "$2", Name: "beta"},
	}})

	e, ok := m.currentSessionEntry()
	if !ok || e.Name != "beta" {
		t.Errorf("currentSessionEntry after reorder = %+v, %v, want it still to be beta", e, ok)
	}
}

func TestWindowsMsgPicksActiveWindow(t *testing.T) {
	m := New(nopRunner(), nil)
	m.sessions = []sessions.Session{{ID: "$1", Name: "alpha"}}
	m.cur[panelSessions] = 0
	windows := []tmux.WindowInfo{
		{Index: 0, ID: "@1", Active: false, Name: "one"},
		{Index: 1, ID: "@2", Active: true, Name: "two"},
	}
	m, cmd := update(m, windowsMsg{sessionID: "$1", windows: windows})
	if m.cur[panelWindows] != 1 {
		t.Errorf("windowCur = %d, want 1 (the active window)", m.cur[panelWindows])
	}
	// It should follow up by loading the active window's panes.
	pm, ok := run(cmd).(panesMsg)
	if ok && pm.windowID != "@2" {
		t.Errorf("panesMsg.windowID = %q, want @2", pm.windowID)
	}
}

func TestStaleWindowsMsgIgnored(t *testing.T) {
	m := New(nopRunner(), nil)
	m.sessions = []sessions.Session{{ID: "$1"}, {ID: "$2"}}
	m.cur[panelSessions] = 0 // current session is $1
	before := m.windows
	m, cmd := update(m, windowsMsg{sessionID: "$2", windows: []tmux.WindowInfo{{ID: "@9"}}})
	if len(m.windows) != len(before) {
		t.Errorf("stale windowsMsg for $2 was applied while $1 is current")
	}
	if cmd != nil {
		t.Errorf("stale windowsMsg produced a follow-up command")
	}
}

func TestPreviewMsgSetsContent(t *testing.T) {
	m := New(nopRunner(), nil)
	m.panes = []tmux.PaneInfo{{ID: "%1", Command: "nvim"}}
	m.cur[panelPanes] = 0
	m, _ = update(m, previewMsg{paneID: "%1", content: "hello world"})
	if m.preview != "hello world" {
		t.Errorf("preview = %q, want %q", m.preview, "hello world")
	}
	if !strings.Contains(m.previewTitle, "nvim") {
		t.Errorf("previewTitle = %q, want it to mention the command", m.previewTitle)
	}
}

func TestNavigationResetsChildCursors(t *testing.T) {
	r := funcRunner{fn: func(_ ...string) (string, error) { return "", nil }}
	m := New(r, nil)
	m.focus = panelSessions
	m.sessions = []sessions.Session{{ID: "$1"}, {ID: "$2"}}
	m.cur[panelSessions] = 0
	m.cur[panelWindows] = 3
	m.cur[panelPanes] = 2
	m, cmd := update(m, key("down")) // move to $2
	if m.cur[panelSessions] != 1 {
		t.Fatalf("sessionCur = %d, want 1", m.cur[panelSessions])
	}
	if m.cur[panelWindows] != -1 || m.cur[panelPanes] != -1 {
		t.Errorf("child cursors not reset: windowCur=%d paneCur=%d", m.cur[panelWindows], m.cur[panelPanes])
	}
	if run(cmd) == nil {
		t.Errorf("moving session should trigger a window reload")
	}
}

func TestEnterSetsPendingAttachAndQuits(t *testing.T) {
	m := New(nopRunner(), nil)
	m.focus = panelSessions
	m.sessions = []sessions.Session{{ID: "$7", Name: "target"}}
	m.cur[panelSessions] = 0
	m, cmd := update(m, key("enter"))
	if m.pendingAttach != "$7" {
		t.Errorf("pendingAttach = %q, want $7", m.pendingAttach)
	}
	if _, ok := run(cmd).(tea.QuitMsg); !ok {
		t.Errorf("enter did not return tea.Quit")
	}
}

func TestSelfPanePreviewSuppressed(t *testing.T) {
	m := New(nopRunner(), nil)
	m.selfPane = "%1"
	m.panes = []tmux.PaneInfo{{ID: "%1", Command: "wyrm"}}
	m.cur[panelPanes] = 0
	cmd := m.reloadPreview()
	if cmd != nil {
		t.Error("reloadPreview should not capture the pane wyrm runs in")
	}
	if m.preview != selfPreviewNotice {
		t.Errorf("preview = %q, want the self-pane notice", m.preview)
	}
	// A different pane still gets a real capture command.
	m.panes = []tmux.PaneInfo{{ID: "%2", Command: "nvim"}}
	if cmd := m.reloadPreview(); cmd == nil {
		t.Error("reloadPreview should capture a pane other than wyrm's own")
	}
}

func TestQuitKeys(t *testing.T) {
	m := New(nopRunner(), nil)
	_, cmd := update(m, key("q"))
	if _, ok := run(cmd).(tea.QuitMsg); !ok {
		t.Errorf("q did not quit")
	}
}

// --- compact (wyrm pick) mode ---

func compactModel() Model {
	m := New(nopRunner(), nil)
	m.compact = true
	m.focus = panelSessions
	m.sessions = []sessions.Session{{ID: "$1", Name: "alpha"}, {ID: "$2", Name: "beta"}}
	m.windows = []tmux.WindowInfo{{Index: 0, ID: "@1", Name: "code"}}
	m.cur[panelSessions], m.cur[panelWindows] = 0, 0
	m.width, m.height, m.ready = 100, 40, true
	return m
}

// Compact mode shows Sessions over Windows and nothing else — the Projects and
// Panes panels are browsing tools that `wyrm pick` has no use for.
func TestCompactShowsOnlySessionsAndWindows(t *testing.T) {
	m := compactModel()
	if got := m.panels(); len(got) != 2 || got[0] != panelSessions || got[1] != panelWindows {
		t.Fatalf("panels() = %v, want [sessions windows]", got)
	}
	if m.panelIndex(panelProjects) != -1 || m.panelIndex(panelPanes) != -1 {
		t.Error("Projects/Panes must not appear in the compact panel set")
	}
}

// Tab cycles within the shown panels rather than walking into ones that aren't
// rendered — the bug a fixed "% numPanels" would reintroduce.
func TestCompactFocusCyclesOnlyShownPanels(t *testing.T) {
	m := compactModel()
	seen := map[panel]bool{}
	for i := 0; i < 6; i++ {
		next, _ := m.cycleFocus(1)
		m = next.(Model)
		seen[m.focus] = true
	}
	if seen[panelProjects] || seen[panelPanes] {
		t.Errorf("focus reached a hidden panel: %v", seen)
	}
	if !seen[panelSessions] || !seen[panelWindows] {
		t.Errorf("focus did not reach both shown panels: %v", seen)
	}
	// And backwards.
	back, _ := m.cycleFocus(-1)
	if p := back.(Model).focus; p != panelSessions && p != panelWindows {
		t.Errorf("shift-tab focus = %v, want a shown panel", p)
	}
}

// The digit keys address the shown panels, so "1" is Sessions in compact mode
// and "3"/"4" do nothing rather than focusing a panel that isn't drawn.
func TestCompactDigitKeysAddressShownPanels(t *testing.T) {
	m := compactModel()
	m, _ = update(m, key("1"))
	if m.focus != panelSessions {
		t.Errorf("'1' focused %v, want Sessions", m.focus)
	}
	m, _ = update(m, key("2"))
	if m.focus != panelWindows {
		t.Errorf("'2' focused %v, want Windows", m.focus)
	}
	m, _ = update(m, key("4"))
	if m.focus != panelWindows {
		t.Errorf("'4' focused %v, want the focus left alone", m.focus)
	}
}

// The full view still maps its four digits, so unifying the two UIs didn't cost
// the TUI anything.
func TestFullDigitKeysStillAddressFourPanels(t *testing.T) {
	m := New(nopRunner(), nil)
	m.width, m.height, m.ready = 100, 40, true
	for i, want := range []panel{panelProjects, panelSessions, panelWindows, panelPanes} {
		m, _ = update(m, key(string(rune('1'+i))))
		if m.focus != want {
			t.Errorf("'%d' focused %v, want %v", i+1, m.focus, want)
		}
	}
}

// Compact mode renders two boxes, and fits in a terminal too short for four.
func TestCompactRendersTwoPanelsAndFitsShorterTerminals(t *testing.T) {
	m := compactModel()
	full := New(nopRunner(), nil)
	if m.minHeight() >= full.minHeight() {
		t.Errorf("compact minHeight %d should be below the full view's %d", m.minHeight(), full.minHeight())
	}

	out := m.View()
	if !strings.Contains(out, "Sessions") || !strings.Contains(out, "Windows") {
		t.Errorf("compact view missing a panel title:\n%s", out)
	}
	if strings.Contains(out, "Projects") || strings.Contains(out, "Panes") {
		t.Errorf("compact view drew a hidden panel:\n%s", out)
	}
}

// The geometry the mouse hit test reads must describe the two boxes actually
// drawn, or a click lands on the wrong row — the failure layout.go exists to
// prevent.
func TestCompactHitTestMatchesRenderedPanels(t *testing.T) {
	m := compactModel()
	g := m.geometry()
	if len(g.panels) != 2 || len(g.boxes) != 2 || len(g.heights) != 2 {
		t.Fatalf("geometry describes %d panels/%d boxes/%d heights, want 2 each",
			len(g.panels), len(g.boxes), len(g.heights))
	}
	total := 0
	for _, h := range g.heights {
		total += h
	}
	if total != g.bodyH {
		t.Errorf("panel heights sum to %d, want the body height %d", total, g.bodyH)
	}
	// The first list row of the top panel selects index 0 of that panel.
	top, _ := g.boxes[0].listRows()
	h, ok := m.hitTest(1, top)
	if !ok || h.panel != panelSessions || h.row != 0 {
		t.Errorf("hitTest at the first Sessions row = %+v (ok=%v), want sessions row 0", h, ok)
	}
}

// In the compact picker the filter is the whole interaction, so Enter finishes
// the job. Requiring a second Enter to attach would be a step backwards from
// the fzf-style chooser `wyrm pick` replaced.
func TestCompactFilterEnterAttaches(t *testing.T) {
	m := compactModel()
	m.mode, m.filtering = modeFilter, true
	for _, r := range "bet" {
		m, _ = update(m, key(string(r)))
	}
	if got := m.visibleSessions(); len(got) != 1 || got[0].Name != "beta" {
		t.Fatalf("filter left %+v, want just beta", got)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeNormal {
		t.Error("enter should close the filter")
	}
	if m.pendingAttach != "$2" {
		t.Errorf("pendingAttach = %q, want beta's $2 — one enter should attach", m.pendingAttach)
	}
}

// The full TUI keeps lazygit's behavior: `/` is one tool among several, so
// Enter commits the search rather than acting on it.
func TestFullFilterEnterOnlyClosesTheFilter(t *testing.T) {
	m := New(nopRunner(), nil)
	m.sessions = []sessions.Session{{ID: "$1", Name: "alpha"}, {ID: "$2", Name: "beta"}}
	m.cur[panelSessions], m.focus = 0, panelSessions
	m.width, m.height, m.ready = 100, 40, true
	m, _ = update(m, key("/"))
	for _, r := range "bet" {
		m, _ = update(m, key(string(r)))
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeNormal {
		t.Error("enter should close the filter")
	}
	if m.pendingAttach != "" {
		t.Errorf("pendingAttach = %q, want empty — the full TUI needs a second enter", m.pendingAttach)
	}
}

// A session manager in a background tab used to keep spending a capture-pane
// every second and a full list sweep every three, forever, for a screen nobody
// was looking at. The tickers still run — losing them would mean never waking
// up again — but they do no work while blurred.
func TestTickersIdleWhileBlurred(t *testing.T) {
	m := New(nopRunner(), nil)
	m.panes = []tmux.PaneInfo{{ID: "%1", Command: "nvim"}}
	m.cur[panelPanes] = 0
	m.previewSrc = previewPane

	m, _ = update(m, tea.BlurMsg{})
	if !m.blurred {
		t.Fatal("BlurMsg did not mark the model blurred")
	}

	// The preview tick reschedules itself but captures nothing.
	blurredModel, cmd := m.Update(tickMsg{})
	m = blurredModel.(Model)
	if cmd == nil {
		t.Error("the ticker must keep rescheduling, or it never wakes again")
	}
	if msgs := drain(cmd); containsPreview(msgs) {
		t.Errorf("a blurred tick captured a pane: %#v", msgs)
	}

	// The list tick likewise.
	_, listCmd := m.Update(listTickMsg{})
	if msgs := drain(listCmd); containsSessions(msgs) {
		t.Errorf("a blurred list tick re-listed sessions: %#v", msgs)
	}

	// Focus returns: refresh at once rather than making the user wait a tick.
	focused, fcmd := m.Update(tea.FocusMsg{})
	if focused.(Model).blurred {
		t.Error("FocusMsg did not clear the blurred flag")
	}
	if msgs := drain(fcmd); !containsSessions(msgs) {
		t.Errorf("regaining focus did not refresh the lists: %#v", msgs)
	}
}

// While focused, the same ticks do their work — the guard must not be
// permanently on.
func TestTickersWorkWhileFocused(t *testing.T) {
	m := New(nopRunner(), nil)
	m.panes = []tmux.PaneInfo{{ID: "%1", Command: "nvim"}}
	m.cur[panelPanes] = 0
	m.previewSrc = previewPane

	_, cmd := m.Update(tickMsg{})
	if msgs := drain(cmd); !containsPreview(msgs) {
		t.Errorf("a focused tick did not capture the pane: %#v", msgs)
	}
	_, listCmd := m.Update(listTickMsg{})
	if msgs := drain(listCmd); !containsSessions(msgs) {
		t.Errorf("a focused list tick did not re-list sessions: %#v", msgs)
	}
}

// drain runs a command (flattening a tea.Batch) and collects the messages it
// produced, skipping the timer commands that never resolve synchronously.
//
// Every call goes through settle, including the outer one: tea.Batch collapses
// to its single element when the rest are nil, so a tick that produced no work
// arrives here as a bare tea.Tick rather than wrapped in a BatchMsg.
func drain(cmd tea.Cmd) []tea.Msg {
	msg, ok := settle(cmd)
	if !ok {
		return nil
	}
	batch, isBatch := msg.(tea.BatchMsg)
	if !isBatch {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if m, ok := settle(c); ok {
			out = append(out, m)
		}
	}
	return out
}

// settle runs cmd and returns its message, or ok=false if it didn't resolve
// promptly — which means it is a tea.Tick waiting out a second or three. The
// commands these tests assert on run their tmux call inline and return at once.
func settle(cmd tea.Cmd) (tea.Msg, bool) {
	if cmd == nil {
		return nil, false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case m := <-done:
		return m, true
	case <-time.After(30 * time.Millisecond):
		return nil, false
	}
}

func containsPreview(msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(previewMsg); ok {
			return true
		}
	}
	return false
}

func containsSessions(msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(sessionsMsg); ok {
			return true
		}
		if _, ok := m.(listsMsg); ok {
			return true
		}
	}
	return false
}

// TestProjectsSelectionDoesNotResetTheCascade guards a mistake that is easy to
// make once the cascade is table-driven: treating "every panel after this one"
// as the child set.
//
// Projects is a sibling list of configs, not the parent of the running
// Sessions. Moving it changes the preview and nothing else — resetting the
// session/window/pane cursors would yank the user off the session they had
// selected just because they scrolled the config list.
func TestProjectsSelectionDoesNotResetTheCascade(t *testing.T) {
	m := New(nopRunner(), nil)
	m.focus = panelProjects
	m.projects = []Project{{Name: "a", Path: "/a"}, {Name: "b", Path: "/b"}}
	m.sessions = []sessions.Session{{ID: "$1"}, {ID: "$2"}}
	m.windows = []tmux.WindowInfo{{ID: "@1"}, {ID: "@2"}}
	m.panes = []tmux.PaneInfo{{ID: "%1"}, {ID: "%2"}}
	m.cur = [numPanels]int{panelProjects: 0, panelSessions: 1, panelWindows: 1, panelPanes: 1}

	m, _ = update(m, key("down"))

	if m.cur[panelProjects] != 1 {
		t.Fatalf("projects cursor = %d, want it to have moved", m.cur[panelProjects])
	}
	for _, tc := range []struct {
		p    panel
		name string
	}{
		{panelSessions, "sessions"}, {panelWindows, "windows"}, {panelPanes, "panes"},
	} {
		if m.cur[tc.p] != 1 {
			t.Errorf("%s cursor = %d, want 1 — moving Projects must not reset it", tc.name, m.cur[tc.p])
		}
	}
}

// The real cascade still has to work: Sessions resets Windows and Panes, and
// Windows resets Panes.
func TestSessionAndWindowSelectionResetTheirChildren(t *testing.T) {
	base := func() Model {
		m := New(nopRunner(), nil)
		m.sessions = []sessions.Session{{ID: "$1"}, {ID: "$2"}}
		m.windows = []tmux.WindowInfo{{ID: "@1"}, {ID: "@2"}}
		m.panes = []tmux.PaneInfo{{ID: "%1"}, {ID: "%2"}}
		m.cur = [numPanels]int{panelSessions: 0, panelWindows: 1, panelPanes: 1}
		return m
	}

	m := base()
	m.focus = panelSessions
	m, _ = update(m, key("down"))
	if m.cur[panelWindows] != -1 || m.cur[panelPanes] != -1 {
		t.Errorf("after moving Sessions: windows=%d panes=%d, want both -1",
			m.cur[panelWindows], m.cur[panelPanes])
	}

	m = base()
	m.focus = panelWindows
	m.cur[panelWindows] = 0
	m, _ = update(m, key("down"))
	if m.cur[panelPanes] != -1 {
		t.Errorf("after moving Windows: panes=%d, want -1", m.cur[panelPanes])
	}
	if m.cur[panelSessions] != 0 {
		t.Errorf("moving Windows changed the Sessions cursor to %d", m.cur[panelSessions])
	}
}
