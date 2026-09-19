// Package tui implements wyrm's interactive, full-screen session manager
// (wyrm tui): a lazygit-style multi-panel view over the running tmux world.
//
// Four stacked panels on the left (Projects -> Sessions -> Windows -> Panes)
// drive a live pane-content preview on the right. Enter attaches to the
// selected session, and management actions (kill/rename/new-window, plus
// building or editing a project's config) are available directly from the
// relevant panel.
//
// The model follows the repo convention of taking a tmux.Runner rather than
// shelling out directly, so Update stays pure and unit-testable: every tmux
// call happens inside a tea.Cmd closure that captures the Runner, and Update
// only ever reacts to the resulting messages.
package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/clipboard"
	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// refreshInterval is how often the selected pane's preview is re-captured so
// the view stays live.
const refreshInterval = time.Second

// listRefreshInterval is how often the project and session lists are re-read.
// Slower than the preview because it costs a tmux round trip per level of the
// cascade, but not never: a session manager that showed a live preview of a
// pane while its session list went stale — including sessions started or ended
// in another terminal — had the two exactly backwards.
const listRefreshInterval = 3 * time.Second

// selfPreviewNotice replaces the live capture when the selected pane is the one
// wyrm tui is itself running in: capturing it would render the TUI back into
// its own preview, a mirror-of-a-mirror that thrashes on every refresh.
const selfPreviewNotice = "This pane is running wyrm tui.\nPreview hidden to avoid a feedback loop."

// panel identifies one of the focusable left-column list panels.
type panel int

const (
	panelProjects panel = iota
	panelSessions
	panelWindows
	panelPanes
	numPanels

	// noPanel is "no such panel", used by childOf to end the cascade.
	noPanel panel = -1
)

// previewSource tracks what the main panel is currently showing, so the ticker
// only refreshes a live pane capture (not a static config preview).
type previewSource int

const (
	previewPane previewSource = iota
	previewConfig
)

// clickState remembers the previous mouse press, which is the whole of what a
// double click needs to recognise itself: same row, same panel, recently
// enough. counted is 0 or 1 — the click that would make it 2 is dispatched as
// an activation instead of being counted.
type clickState struct {
	counted int
	at      time.Time
	panel   panel
	row     int
}

// mode is the input mode: normal navigation, a yes/no confirm modal (for
// destructive kills), a text-entry prompt (rename / new-window), the help
// overlay, or type-to-filter.
type mode int

const (
	modeNormal mode = iota
	modeConfirm
	modePrompt
	modeHelp
	modeFilter
	// modeMenu is the right-click context menu. It's a mode rather than an
	// overlay flag because it captures the keys it shares with normal mode
	// (j/k/enter) while it's open.
	modeMenu
	// modeFindPane is the flat, whole-server pane search ("f") — every pane
	// on every session in one filterable list, the thing tmux's own
	// choose-tree -Z gives you and the panel hierarchy (Sessions -> Windows
	// -> Panes) doesn't: reaching a pane three levels deep still means
	// drilling down one panel at a time. It's an overlay like modeHelp, not
	// a fifth panel — it doesn't fit the Projects/Sessions/Windows/Panes
	// cascade's parent-child relationships, and full-TUI only (wyrm pick's
	// compact form is deliberately the two-panel Sessions/Windows chooser).
	modeFindPane
	// modePager is the full scrollback buffer pager and search mode ("p" / "[").
	modePager
	// modeMoveWindow is the cross-session window transfer picker ("W").
	modeMoveWindow
)

// Model is the Bubble Tea model for the TUI. It is a plain value type; Update
// returns an updated copy. tmux access is confined to the command closures, so
// the model itself holds only data and view state.
type Model struct {
	runner   tmux.Runner
	settings *config.Settings

	focus panel

	projects []Project
	sessions []sessions.Session
	windows  []tmux.WindowInfo
	panes    []tmux.PaneInfo

	// cur is each panel's selected row, indexed by panel.
	//
	// The four lists above stay separately typed — they hold genuinely
	// different things — but the cursors are four ints with identical
	// semantics, and holding them apart meant every question about "the
	// selection" became a switch. Two of those switches (cursorFor and
	// focusedCursor) were the same switch written twice.
	//
	// -1 means "not chosen yet": the first load of a panel then snaps to
	// whatever tmux says is active rather than to row 0.
	cur [numPanels]int

	preview      string
	previewTitle string
	previewSrc   previewSource

	width, height int
	ready         bool

	// selfPane is the tmux pane ID wyrm tui is running in ($TMUX_PANE), or ""
	// when launched outside tmux. Its preview is suppressed to avoid a mirror
	// loop.
	selfPane string

	// modal state.
	mode          mode
	pending       pendingAction
	confirmPrompt string          // shown in modeConfirm
	promptTitle   string          // label shown in modePrompt
	textInput     textinput.Model // active in modePrompt
	helpScroll    int             // top line offset of the help overlay (modeHelp)

	// findPane* backs modeFindPane: the whole-server pane list, the typed
	// filter (typing is always "on" here, unlike modeFilter's separate
	// filtering flag — the mode has no other purpose), and the selected row
	// in the filtered list. Kept apart from the four panel lists/cur array
	// since this isn't one of the panelSpecs-described panels.
	allPanes      []tmux.PaneRef
	findPaneQuery string
	findPaneCur   int

	// pager* backs modePager: full scrollback viewer and buffer search.
	pagerPaneID    string
	pagerPaneTitle string
	pagerContent   string
	pagerLines     []string
	pagerScroll    int
	pagerSearching bool
	pagerQuery     string
	pagerMatches   []int
	pagerMatchIdx  int

	// info is a transient status notification shown in the footer (e.g. on clipboard copy).
	info string

	// prevPaneStates tracks agent pane states across scans to trigger notifications on change.
	prevPaneStates map[string]agent.State

	// layoutIdx rotates through cycleLayouts on "L", and layoutWindow is the
	// window it belongs to. Keeping them together makes the cycle per-window:
	// a single shared index meant switching windows resumed mid-cycle, so the
	// first "L" often re-applied the layout that window already had and looked
	// like it had done nothing.
	layoutIdx    int
	layoutWindow string

	// filter narrows the focused panel (modeFilter types into it). Only the
	// focused panel is filtered, so the rest of the cascade stays readable.
	filter    string
	filtering bool

	// allSessions widens the Sessions panel from "what tmux is running" to
	// "everything I could be in", folding the not-running projects in as
	// startable rows — see sessionEntries. Off by default: the panel's job in
	// the four-panel cascade is the live tmux world, and Projects already
	// stands beside it. It earns its keep in `wyrm pick`, which shows no
	// Projects panel and so otherwise cannot start a stopped session at all.
	allSessions bool

	// agents holds the last agent-pane scan: which sessions, windows, and panes
	// hold an AI agent that's waiting on the user. Empty until the first scan.
	agents agentStatus

	// agentProfiles is the compiled detector configuration, resolved once at
	// startup rather than per scan — it involves compiling regexps, and the scan
	// runs on a timer.
	agentProfiles []agent.Profile

	// blurred is set while the terminal reports the window as unfocused. The
	// refresh tickers keep running but skip their work — see the tickMsg case.
	blurred bool

	// compact drops the Projects and Panes panels, leaving Sessions over
	// Windows. It is what `wyrm pick` runs — see RunPicker. Focus and the mouse
	// hit test both walk panels(), so nothing has to special-case it beyond the
	// panel list itself.
	compact bool

	// mouseOn mirrors the terminal's mouse-reporting state, toggled by "m".
	// Capturing the mouse takes click-drag text selection away from the
	// terminal, so it has to be surrenderable without restarting the TUI.
	mouseOn bool

	// lastClick is what a double click is measured against — see clickState.
	lastClick clickState

	// clock reads the current time, so tests can drive the double-click window
	// without sleeping. nil means time.Now — see Model.now.
	clock func() time.Time

	// The context menu (modeMenu). menuX/menuY are the click point it's
	// anchored to, not its top-left corner — see menuBox.
	menu         []menuEntry
	menuCur      int
	menuX, menuY int

	err error

	// modeMoveWindow picker state
	moveWindowSessions     []sessions.Session
	moveWindowCur          int
	moveWindowSrcID        string
	moveWindowSrcSessionID string

	// pendingAttach is the tmux session ID (e.g. "$3") to hand the terminal to
	// once the program exits. The alt-screen program can't attach in-place, so
	// runTUI performs the attach after Run returns — mirroring runPicker.
	pendingAttach string
}

// New builds a Model backed by runner. The Windows and Panes cursors start at
// -1 so the first load of each snaps to the active window/pane rather than
// index 0.
// Focus starts on Sessions rather than the first panel: the common reason to
// open the TUI is to reach a running session, and it makes the initial preview
// a live pane capture instead of a config file.
// settings may be nil (the Projects panel then lists only local configs).
func New(runner tmux.Runner, settings *config.Settings) Model {
	// A broken profile is reported by Run before the alt screen opens; New
	// itself falls back to the built-in one so the zero-config and test paths
	// stay total.
	profiles, err := agentProfiles(settings)
	if err != nil {
		profiles = []agent.Profile{agent.DefaultProfile()}
	}
	return Model{
		runner:        runner,
		settings:      settings,
		focus:         panelSessions,
		cur:           [numPanels]int{panelWindows: -1, panelPanes: -1},
		selfPane:      os.Getenv("TMUX_PANE"),
		mouseOn:       settings.MouseEnabled(),
		agentProfiles: profiles,
	}
}

// Init loads the initial project and session lists and starts the refresh
// ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(append(m.refreshLists(), tick(), listTick())...)
}

// refreshLists re-reads everything the left column shows. Four places want
// exactly this — startup, the "R" key, regaining terminal focus, and the list
// ticker — and they were four copies of the same command set.
//
// It returns the commands rather than a tea.Batch of them so callers can splice
// in their own without nesting one batch inside another.
func (m Model) refreshLists() []tea.Cmd {
	return []tea.Cmd{loadProjects(m.runner, m.settings), loadSessions(m.runner), m.agentCmd()}
}

// --- messages ---

type sessionsMsg struct {
	sessions []sessions.Session
	err      error
}

type windowsMsg struct {
	sessionID string
	windows   []tmux.WindowInfo
	err       error
}

type panesMsg struct {
	windowID string
	panes    []tmux.PaneInfo
	err      error
}

type previewMsg struct {
	paneID  string
	content string
	err     error
}

// allPanesMsg carries a fresh whole-server pane listing for modeFindPane.
type allPanesMsg struct {
	panes []tmux.PaneRef
	err   error
}

// pagerMsg carries the full captured scrollback buffer for modePager.
type pagerMsg struct {
	paneID  string
	title   string
	content string
	err     error
}

type tickMsg time.Time

// listTickMsg drives the slower project/session refresh — see
// listRefreshInterval.
type listTickMsg time.Time

// --- commands ---

func loadSessions(r tmux.Runner) tea.Cmd {
	return func() tea.Msg {
		s, err := sessions.List(r)
		return sessionsMsg{sessions: s, err: err}
	}
}

func loadWindows(r tmux.Runner, sessionID string) tea.Cmd {
	return func() tea.Msg {
		w, err := tmux.ListWindows(r, sessionID)
		return windowsMsg{sessionID: sessionID, windows: w, err: err}
	}
}

func loadPanes(r tmux.Runner, windowID string) tea.Cmd {
	return func() tea.Msg {
		p, err := tmux.ListPanes(r, windowID)
		return panesMsg{windowID: windowID, panes: p, err: err}
	}
}

func loadPreview(r tmux.Runner, paneID string) tea.Cmd {
	return func() tea.Msg {
		out, err := tmux.CapturePane(r, paneID)
		return previewMsg{paneID: paneID, content: out, err: err}
	}
}

func loadPager(r tmux.Runner, paneID, title string) tea.Cmd {
	return func() tea.Msg {
		out, err := tmux.CapturePaneFull(r, paneID)
		return pagerMsg{paneID: paneID, title: title, content: out, err: err}
	}
}

// loadAllPanes fetches the whole-server pane list for modeFindPane, in the
// one round trip tmux.ListAllPanes already costs the agent scan.
func loadAllPanes(r tmux.Runner) tea.Cmd {
	return func() tea.Msg {
		refs, err := tmux.ListAllPanes(r)
		return allPanesMsg{panes: refs, err: err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func listTick() tea.Cmd {
	return tea.Tick(listRefreshInterval, func(t time.Time) tea.Msg { return listTickMsg(t) })
}

// --- filtering ---

// filterFor returns the filter text in force for a panel. Only the focused
// panel is filtered: narrowing every panel at once would hide the session a
// window belongs to, which is exactly the context the cascade exists to show.
func (m Model) filterFor(p panel) string {
	if m.focus != p {
		return ""
	}
	return m.filter
}

// visible* return the rows a panel actually displays. Cursors index these, not
// the unfiltered slices, so the selection always means what's under it.

func (m Model) visibleProjects() []Project {
	f := m.filterFor(panelProjects)
	if f == "" {
		return m.projects
	}
	out := make([]Project, 0, len(m.projects))
	for _, p := range m.projects {
		if _, ok := sessions.FuzzyMatch(f, p.Name); ok {
			out = append(out, p)
		}
	}
	return out
}

func (m Model) visibleSessions() []sessionEntry {
	entries := m.sessionEntries()
	f := m.filterFor(panelSessions)
	if f == "" {
		return entries
	}
	out := make([]sessionEntry, 0, len(entries))
	for _, e := range entries {
		if _, ok := sessions.FuzzyMatch(f, e.Name); ok {
			out = append(out, e)
		}
	}
	return out
}

func (m Model) visibleWindows() []tmux.WindowInfo {
	f := m.filterFor(panelWindows)
	if f == "" {
		return m.windows
	}
	out := make([]tmux.WindowInfo, 0, len(m.windows))
	for _, w := range m.windows {
		if _, ok := sessions.FuzzyMatch(f, w.Name); ok {
			out = append(out, w)
		}
	}
	return out
}

func (m Model) visiblePanes() []tmux.PaneInfo {
	f := m.filterFor(panelPanes)
	if f == "" {
		return m.panes
	}
	out := make([]tmux.PaneInfo, 0, len(m.panes))
	for _, p := range m.panes {
		if _, ok := sessions.FuzzyMatch(f, p.ID+" "+p.Command); ok {
			out = append(out, p)
		}
	}
	return out
}

// visibleAllPanes narrows the whole-server pane list (modeFindPane) by
// findPaneQuery. Matched against session name, window name, and command
// together, since that's the context a flat list needs to disambiguate
// what would otherwise be identical-looking rows across sessions.
func (m Model) visibleAllPanes() []tmux.PaneRef {
	if m.findPaneQuery == "" {
		return m.allPanes
	}
	out := make([]tmux.PaneRef, 0, len(m.allPanes))
	for _, p := range m.allPanes {
		target := p.SessionName + " " + p.WindowName + " " + p.Command
		if _, ok := sessions.FuzzyMatch(m.findPaneQuery, target); ok {
			out = append(out, p)
		}
	}
	return out
}

// compactPanels is the panel set `wyrm pick` shows: find a session, optionally
// pick the window to land on. Projects and Panes are browsing tools, which is
// what the full TUI is for.
var compactPanels = []panel{panelSessions, panelWindows}

// allPanels is the full cascade, in render order.
var allPanels = []panel{panelProjects, panelSessions, panelWindows, panelPanes}

// panels returns the panels this model shows, in top-to-bottom order. Focus
// cycling, the layout, and the mouse hit test all read it, so "which panels
// exist" is stated once.
func (m Model) panels() []panel {
	if m.compact {
		return compactPanels
	}
	return allPanels
}

// panelIndex is p's position in panels(), or -1 when this model doesn't show it.
func (m Model) panelIndex(p panel) int {
	for i, q := range m.panels() {
		if q == p {
			return i
		}
	}
	return -1
}

// --- selection accessors ---

func (m Model) currentProject() (Project, bool) {
	list := m.visibleProjects()
	if m.cur[panelProjects] < 0 || m.cur[panelProjects] >= len(list) {
		return Project{}, false
	}
	return list[m.cur[panelProjects]], true
}

// currentSession is the selected *running* session. A stopped row (only
// reachable with allSessions on) reports false, which is deliberately the same
// answer as an empty panel: every session-targeting action — attach, rename,
// kill, new-window, the window cascade — already guards on it, so none of them
// needs to learn that a row without a session ID now exists.
func (m Model) currentSession() (sessions.Session, bool) {
	e, ok := m.currentSessionEntry()
	if !ok || !e.Running {
		return sessions.Session{}, false
	}
	return e.Session, true
}

// currentSessionEntry is the selected row whether or not it is running — what
// the renderer, the menu, and Enter need in order to tell the two apart.
func (m Model) currentSessionEntry() (sessionEntry, bool) {
	list := m.visibleSessions()
	if m.cur[panelSessions] < 0 || m.cur[panelSessions] >= len(list) {
		return sessionEntry{}, false
	}
	return list[m.cur[panelSessions]], true
}

func (m Model) currentWindow() (tmux.WindowInfo, bool) {
	list := m.visibleWindows()
	if m.cur[panelWindows] < 0 || m.cur[panelWindows] >= len(list) {
		return tmux.WindowInfo{}, false
	}
	return list[m.cur[panelWindows]], true
}

func (m Model) currentPane() (tmux.PaneInfo, bool) {
	list := m.visiblePanes()
	if m.cur[panelPanes] < 0 || m.cur[panelPanes] >= len(list) {
		return tmux.PaneInfo{}, false
	}
	return list[m.cur[panelPanes]], true
}

// --- selection identity ---
//
// m.cur holds a plain index into each panel's *current* visible list, not an
// item identity. That index silently starts meaning something else — a
// different session, window, or pane than the one actually selected — the
// moment the list it indexes changes shape without the user moving the
// cursor: a periodic refresh reorders or resizes the underlying slice, or
// focus leaves a filtered panel and filterFor stops narrowing it (see
// filterFor). The helpers below re-anchor the index to the same item's new
// position whenever a caller replaces a list or moves focus, so a still-
// present item stays selected and a stale one falls back to the ordinary
// clamp/activeOrClamp behavior.

func indexOfSession(entries []sessionEntry, name string) int {
	for i, e := range entries {
		if e.Name == name {
			return i
		}
	}
	return -1
}

func indexOfWindow(windows []tmux.WindowInfo, id string) int {
	for i, w := range windows {
		if w.ID == id {
			return i
		}
	}
	return -1
}

func indexOfPane(panes []tmux.PaneInfo, id string) int {
	for i, p := range panes {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// rebaseFilteredCursor translates the focused panel's cursor from an index
// into its filtered view to the same item's index in its unfiltered one. Call
// it before moving focus away from the panel: filterFor only narrows the
// *focused* panel, so the instant focus leaves, the same integer index is
// reinterpreted against the full list — selecting whatever row happens to
// sit there rather than the item the user actually filtered down to and
// picked, while any already-loaded child data (e.g. Windows for a Sessions
// selection) still describes the item that really was selected.
func (m Model) rebaseFilteredCursor() Model {
	if m.filterFor(m.focus) == "" {
		return m
	}
	switch m.focus {
	case panelProjects:
		if p, ok := m.currentProject(); ok {
			for i, q := range m.projects {
				if q.Path == p.Path && q.Root == p.Root {
					m.cur[panelProjects] = i
					break
				}
			}
		}
	case panelSessions:
		if e, ok := m.currentSessionEntry(); ok {
			if i := indexOfSession(m.sessionEntries(), e.Name); i >= 0 {
				m.cur[panelSessions] = i
			}
		}
	case panelWindows:
		if w, ok := m.currentWindow(); ok {
			if i := indexOfWindow(m.windows, w.ID); i >= 0 {
				m.cur[panelWindows] = i
			}
		}
	case panelPanes:
		if p, ok := m.currentPane(); ok {
			if i := indexOfPane(m.panes, p.ID); i >= 0 {
				m.cur[panelPanes] = i
			}
		}
	}
	return m
}

// setFocus moves focus to p, first rebasing the outgoing panel's cursor so
// the item selected while it was filtered stays selected once it no longer
// is — see rebaseFilteredCursor.
func (m Model) setFocus(p panel) Model {
	m = m.rebaseFilteredCursor()
	m.focus = p
	return m
}

// --- update ---

// Update is the pure reducer. It never touches tmux or stdio directly; it only
// folds incoming messages into new state and returns follow-up commands.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	case clipboardMsg:
		m.copyResult(msg.what, msg.err)
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case agentStatusMsg:
		// A failed scan keeps the previous markers rather than clearing them or
		// claiming the footer: this is an optional decoration polled on a timer,
		// and one unlucky tmux call shouldn't wipe an error the user is reading.
		if msg.err == nil {
			var notifyCmds []tea.Cmd
			if m.settings != nil && m.settings.AgentNotifyEnabled() && m.prevPaneStates != nil {
				cfg := agent.NotifyConfig{
					Enabled:   m.settings.AgentNotifyEnabled(),
					Desktop:   m.settings.AgentNotifyDesktop(),
					Bell:      m.settings.AgentNotifyBell(),
					OSC:       m.settings.AgentNotifyOSC(),
					OnBlocked: m.settings.AgentNotifyOnBlocked(),
					OnIdle:    m.settings.AgentNotifyOnIdle(),
					Command:   m.settings.AgentNotifyCommand(),
				}
				for paneID, state := range msg.status.panes {
					prevState := m.prevPaneStates[paneID]
					if state != prevState && (state == agent.StateBlocked || state == agent.StateIdle) {
						ref := msg.status.refs[paneID]
						n := agent.Notification{
							State:       state,
							PaneID:      paneID,
							SessionName: ref.SessionName,
							WindowName:  ref.WindowName,
						}
						notifyCmds = append(notifyCmds, notificationCmd(n, cfg))
					}
				}
			}
			m.prevPaneStates = make(map[string]agent.State, len(msg.status.panes))
			for k, v := range msg.status.panes {
				m.prevPaneStates[k] = v
			}
			m.agents = msg.status
			if len(notifyCmds) > 0 {
				return m, tea.Batch(notifyCmds...)
			}
		}
		return m, nil

	case pagerMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.pagerPaneID = msg.paneID
		m.pagerPaneTitle = msg.title
		m.pagerContent = msg.content
		m.pagerLines = strings.Split(msg.content, "\n")
		m.pagerSearching = false
		m.pagerQuery = ""
		m.pagerMatches = nil
		m.pagerMatchIdx = 0
		pageH := m.pagerBodyHeight()
		m.pagerScroll = len(m.pagerLines) - pageH
		if m.pagerScroll < 0 {
			m.pagerScroll = 0
		}
		m.mode = modePager
		return m, nil

	case tea.FocusMsg:
		// Terminal focus came back: refresh immediately rather than making the
		// user wait out the rest of a tick for a stale screen.
		m.blurred = false
		return m, tea.Batch(append(m.refreshLists(), m.reloadPreview())...)

	case tea.BlurMsg:
		m.blurred = true
		return m, nil

	case tickMsg:
		// Refresh the live pane preview, then reschedule. A config preview is
		// static, so leave it alone — keyed off what's actually being shown
		// rather than off which panel has focus.
		//
		// The ticker keeps running while the terminal is unfocused but does no
		// work: a session manager sitting in a background tab was spending a
		// capture-pane every second and a list sweep every three, forever, for a
		// screen nobody was looking at.
		var cmd tea.Cmd
		if !m.blurred && m.previewSrc == previewPane {
			cmd = m.reloadPreview()
		}
		return m, tea.Batch(cmd, tick())

	case listTickMsg:
		// Re-read the lists so sessions started or ended elsewhere show up.
		// Held off while a modal is open, so the data under a confirm prompt
		// can't change between reading it and answering it.
		if m.blurred || (m.mode != modeNormal && m.mode != modeFilter) {
			return m, listTick()
		}
		return m, tea.Batch(append(m.refreshLists(), listTick())...)

	case projectsMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.projects = msg.projects
		m.cur[panelProjects] = clamp(m.cur[panelProjects], m.panelLen(panelProjects))
		if msg.warnings != "" {
			// A non-fatal hook warning from whatever triggered this reload
			// (e.g. killProjectCmd's on_project_exit) — visible, but must not
			// block the list refresh above the way a real err does.
			m.err = errors.New(msg.warnings)
		}
		if m.focus == panelProjects {
			return m, m.updatePreview()
		}
		return m, nil

	case configPreviewMsg:
		if m.previewSrc != previewConfig {
			return m, nil
		}
		if p, ok := m.currentProject(); !ok || p.Path != msg.path {
			return m, nil
		}
		if msg.err != nil {
			m.preview = msg.err.Error()
			return m, nil
		}
		m.preview = msg.content
		return m, nil

	case projectStartedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.pendingAttach = msg.sessionID
		if msg.warnings != "" {
			// A non-fatal build/attach-hook warning — visible via
			// runProgram's post-quit report, but must not block the attach
			// this message is already committed to.
			m.err = errors.New(msg.warnings)
		}
		return m, tea.Quit

	case sessionsMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		// Capture the selected session's identity before the list underneath
		// it is replaced outright: a periodic refresh can add, remove, or
		// reorder sessions with no input from the user, and a bare index
		// would then silently select whichever session ends up in that slot
		// instead of the one actually selected — see rebaseFilteredCursor.
		prevName := ""
		if e, ok := m.currentSessionEntry(); ok {
			prevName = e.Name
		}
		m.sessions = msg.sessions
		m.cur[panelSessions] = clamp(m.cur[panelSessions], m.panelLen(panelSessions))
		if prevName != "" {
			if i := indexOfSession(m.visibleSessions(), prevName); i >= 0 {
				m.cur[panelSessions] = i
			}
		}
		return m, m.reloadWindows()

	case windowsMsg:
		// Ignore a stale response for a session we've since moved off of.
		if s, ok := m.currentSession(); !ok || s.ID != msg.sessionID {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		prevID := ""
		if w, ok := m.currentWindow(); ok {
			prevID = w.ID
		}
		m.windows = msg.windows
		m.cur[panelWindows] = activeOrClamp(m.cur[panelWindows], m.visibleWindows())
		if prevID != "" {
			if i := indexOfWindow(m.visibleWindows(), prevID); i >= 0 {
				m.cur[panelWindows] = i
			}
		}
		return m, m.reloadPanes()

	case panesMsg:
		if w, ok := m.currentWindow(); !ok || w.ID != msg.windowID {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		prevID := ""
		if p, ok := m.currentPane(); ok {
			prevID = p.ID
		}
		m.panes = msg.panes
		m.cur[panelPanes] = activePaneOrClamp(m.cur[panelPanes], m.visiblePanes())
		if prevID != "" {
			if i := indexOfPane(m.visiblePanes(), prevID); i >= 0 {
				m.cur[panelPanes] = i
			}
		}
		return m, m.reloadPreview()

	case allPanesMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.allPanes = msg.panes
		m.findPaneCur = clamp(m.findPaneCur, len(m.visibleAllPanes()))
		return m, nil

	case previewMsg:
		if m.previewSrc != previewPane {
			return m, nil
		}
		if p, ok := m.currentPane(); !ok || p.ID != msg.paneID {
			return m, nil
		}
		if msg.err != nil {
			m.preview = msg.err.Error()
			return m, nil
		}
		m.preview = msg.content
		if p, ok := m.currentPane(); ok {
			m.previewTitle = p.ID + " " + p.Command
		}
		return m, nil

	case actionErrMsg:
		m.err = msg.err
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modePrompt:
		return m.handlePromptKey(msg)
	case modeHelp:
		return m.handleHelpKey(msg)
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeMenu:
		return m.handleMenuKey(msg)
	case modeFindPane:
		return m.handleFindPaneKey(msg)
	case modePager:
		return m.handlePagerKey(msg)
	case modeMoveWindow:
		return m.handleMoveWindowKey(msg)
	}
	// Any key clears a reported error or info notice, so the footer returns to
	// the key hints once it's been seen.
	m.err = nil
	m.info = ""
	return m.handleNormalKey(msg)
}

// handleFilterKey types into the panel filter. The selection is clamped on
// every keystroke, since narrowing the list can put the cursor past its end.
func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.filtering = false
		m.mode = modeNormal
		// In the compact picker the filter *is* the interaction — it opens
		// focused and typing is the first thing you do — so Enter finishes the
		// job rather than just the filter. Requiring a second Enter to attach
		// would be a step backwards from the fzf-style chooser this replaced.
		// The full TUI keeps the lazygit behavior: there `/` is one tool among
		// several and Enter should commit the search, not act on it.
		if m.compact {
			return m.activateSelection()
		}
		return m, nil
	case tea.KeyEsc, tea.KeyCtrlC:
		m.filtering = false
		m.filter = ""
		m.mode = modeNormal
		return m.clampFocused()
	case tea.KeyBackspace:
		m.filter = trimLastRune(m.filter)
		return m.clampFocused()
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(msg.Runes)
		if msg.Type == tea.KeySpace {
			m.filter += " "
		}
		return m.clampFocused()
	}
	return m, nil
}

// clampFocused keeps the focused panel's cursor inside its visible list and
// reloads whatever depends on it.
func (m Model) clampFocused() (tea.Model, tea.Cmd) {
	m.cur[m.focus] = clamp(m.cur[m.focus], m.panelLen(m.focus))
	return m, m.selectionChanged(m.focus)
}

// handleFindPaneKey drives modeFindPane: typing narrows the whole-server
// pane list immediately (there is no separate "browsing, not filtering"
// state here, unlike the panel filter), arrows move the selection, Enter
// attaches directly to the selected pane's session/window/pane, and Esc
// drops back to the normal panel hierarchy.
func (m Model) handleFindPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		return m.attachToFindPaneSelection()
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeNormal
		m.findPaneQuery = ""
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.findPaneCur = clampTo(m.findPaneCur-1, len(m.visibleAllPanes()))
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.findPaneCur = clampTo(m.findPaneCur+1, len(m.visibleAllPanes()))
		return m, nil
	case tea.KeyBackspace:
		m.findPaneQuery = trimLastRune(m.findPaneQuery)
		m.findPaneCur = clamp(m.findPaneCur, len(m.visibleAllPanes()))
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		m.findPaneQuery += string(msg.Runes)
		if msg.Type == tea.KeySpace {
			m.findPaneQuery += " "
		}
		m.findPaneCur = clamp(m.findPaneCur, len(m.visibleAllPanes()))
		return m, nil
	}
	return m, nil
}

// attachToFindPaneSelection resolves the selected row in modeFindPane
// straight to its session/window/pane, the same selectTargetCmd sequence
// Enter on the Panes panel already uses — just addressed directly instead
// of via the cascade's own selected-session/selected-window chain.
func (m Model) attachToFindPaneSelection() (tea.Model, tea.Cmd) {
	list := m.visibleAllPanes()
	if m.findPaneCur < 0 || m.findPaneCur >= len(list) {
		return m, nil
	}
	ref := list[m.findPaneCur]
	m.pendingAttach = ref.SessionID
	m.mode = modeNormal
	return m, tea.Sequence(selectTargetCmd(m.runner, ref.WindowID, ref.PaneID), tea.Quit)
}

// handleHelpKey scrolls the help overlay or closes it. Only esc/q/?/Ctrl-C
// close; navigation keys scroll so a taller-than-screen cheat sheet stays fully
// reachable.
func (m Model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "?", "ctrl+c":
		m.mode = modeNormal
		m.helpScroll = 0
		return m, nil
	case "down", "j":
		m.helpScroll++
	case "up", "k":
		m.helpScroll--
	case "pgdown", "ctrl+d", "f", " ":
		m.helpScroll += m.helpVisible() - 1
	case "pgup", "ctrl+u", "b":
		m.helpScroll -= m.helpVisible() - 1
	case "g", "home":
		m.helpScroll = 0
	case "G", "end":
		m.helpScroll = m.helpMaxScroll()
	}
	if maxScroll := m.helpMaxScroll(); m.helpScroll > maxScroll {
		m.helpScroll = maxScroll
	}
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
	return m, nil
}

func (m Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "R":
		// An explicit reload means now, so drop the wildcard match cache the
		// timed refreshes ride on — otherwise a directory created a moment ago
		// stays invisible for up to wildcardCacheTTL despite the user asking.
		config.InvalidateWildcardCache()
		return m, tea.Batch(m.refreshLists()...)
	case "m":
		// Hand the mouse back to the terminal (and take it again). While it's
		// captured, click-drag selects nothing — this is the way out for anyone
		// who wants to copy text off the screen.
		m.mouseOn = !m.mouseOn
		return m, m.mouseCmd()
	case "M":
		// The keyboard route to the right-click menu — see openMenuAtSelection
		// for why the mouse can't be the only way in.
		return m.openMenuAtSelection()
	case "tab", "l", "right":
		return m.cycleFocus(1)
	case "shift+tab", "h", "left":
		return m.cycleFocus(-1)
	case "1", "2", "3", "4":
		// The digits address the panels this model actually shows, so in compact
		// mode "1" is Sessions rather than a Projects panel that isn't there.
		return m.focusPanelAt(int(msg.String()[0] - '1'))
	case "up", "k":
		return m.moveCursor(-1)
	case "down", "j":
		return m.moveCursor(1)
	case "pgup", "ctrl+u":
		return m.moveCursor(-m.pageSize())
	case "pgdown", "ctrl+d":
		return m.moveCursor(m.pageSize())
	case "g", "home":
		return m.moveCursorTo(0)
	case "G", "end":
		return m.moveCursorTo(m.panelLen(m.focus) - 1)
	case "/":
		m.mode = modeFilter
		m.filtering = true
		return m, nil
	case "esc":
		if m.filter != "" {
			m.filter = ""
			return m.clampFocused()
		}
		return m, nil
	case "enter":
		return m.activateSelection()
	case "a":
		// Scoped to the panel it changes, like "e" on Projects.
		if m.focus != panelSessions {
			return m, nil
		}
		m.allSessions = !m.allSessions
		// The row under the cursor moves when the list grows or shrinks, so
		// the window cascade has to follow it — clampFocused is exactly that.
		return m.clampFocused()
	case "x":
		return m.startKill()
	case "r":
		return m.startRename()
	case "n":
		// Scoped to the panels whose footer advertises it. These used to fire
		// from any panel, acting on a selection in a panel that didn't have
		// focus while the contextual help said otherwise.
		if m.focus != panelSessions && m.focus != panelWindows {
			return m, nil
		}
		return m.startNewWindow()
	case "<", "K":
		if m.focus != panelWindows {
			return m, nil
		}
		return m.swapWindow(-1)
	case ">", "J":
		if m.focus != panelWindows {
			return m, nil
		}
		return m.swapWindow(1)
	case "w", "W":
		if m.focus != panelWindows {
			return m, nil
		}
		return m.startMoveWindow()
	case "s", "v":
		if m.focus != panelWindows && m.focus != panelPanes {
			return m, nil
		}
		return m.startSplitPane(false)
	case "S":
		if m.focus != panelWindows && m.focus != panelPanes {
			return m, nil
		}
		return m.startSplitPane(true)
	case "L":
		if m.focus != panelWindows {
			return m, nil
		}
		return m.cycleLayout()
	case "z":
		if m.focus != panelPanes {
			return m, nil
		}
		return m.zoomPane()
	case "e":
		if m.focus != panelProjects {
			return m, nil
		}
		return m.editProject()
	case "?":
		m.mode = modeHelp
		m.helpScroll = 0
		return m, nil
	case "f":
		// Full TUI only — wyrm pick's compact form is deliberately the
		// two-panel Sessions/Windows chooser, and a whole-server pane
		// search doesn't fit that scope.
		if m.compact {
			return m, nil
		}
		m.mode = modeFindPane
		m.findPaneQuery = ""
		m.findPaneCur = 0
		return m, loadAllPanes(m.runner)
	case "p", "P", "[":
		if p, ok := m.currentPane(); ok {
			title := p.ID + " " + p.Command
			if s, sok := m.currentSession(); sok {
				title = s.Name + ": " + title
			}
			return m, loadPager(m.runner, p.ID, title)
		}
		return m, nil
	case "y":
		switch m.focus {
		case panelSessions:
			if s, ok := m.currentSession(); ok {
				return m, copyCmd(s.Name, fmt.Sprintf("session %q", s.Name))
			}
		case panelProjects:
			if p, ok := m.currentProject(); ok {
				path := p.Path
				if path == "" {
					path = p.Root
				}
				return m, copyCmd(path, fmt.Sprintf("project path %q", path))
			}
		case panelWindows:
			if w, ok := m.currentWindow(); ok {
				return m, copyCmd(w.Name, fmt.Sprintf("window %q", w.Name))
			}
		case panelPanes:
			if p, ok := m.currentPane(); ok {
				return m, copyCmd(m.preview, fmt.Sprintf("pane %s preview", p.ID))
			}
		}
		return m, nil
	}
	return m, nil
}

// copy puts text on the system clipboard and reports what actually happened.
//
// Every call site used to discard the error and announce success regardless,
// which on a host with no clipboard tool meant "y" printed "copied ..." and did
// nothing at all. There is no OSC 52 fallback here on purpose: it would have to
// be written to the terminal, which Bubble Tea owns while the TUI is up — see
// clipboard.OSC52.
// clipboardWrite is clipboard.Write behind a variable so a test can exercise
// the copy keys on a machine that has no clipboard tool at all — CI runners
// have none, and Model must stay comparable, so this cannot be a func field.
var clipboardWrite = clipboard.Write

type clipboardMsg struct {
	what string
	err  error
}

func copyCmd(text, what string) tea.Cmd {
	write := clipboardWrite
	return func() tea.Msg { return clipboardMsg{what: what, err: write(text)} }
}

func (m *Model) copyResult(what string, err error) {
	if err != nil {
		if errors.Is(err, clipboard.ErrNoBackend) {
			m.info = "cannot copy: no clipboard tool found (install " + clipboard.Backends() + ")"
			return
		}
		m.info = "cannot copy " + what + ": " + err.Error()
		return
	}
	m.info = "copied " + what + " to clipboard"
}

// handlePagerKey drives modePager: scrolling the full scrollback buffer, searching
// via /, jumping to matches with n/N, copying with y, and exiting with q/Esc.
func (m Model) handlePagerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pagerSearching {
		switch msg.Type {
		case tea.KeyEnter:
			m.pagerSearching = false
			return m, nil
		case tea.KeyEsc, tea.KeyCtrlC:
			m.pagerSearching = false
			m.pagerQuery = ""
			m.pagerMatches = nil
			m.pagerMatchIdx = 0
			return m, nil
		case tea.KeyBackspace:
			m.pagerQuery = trimLastRune(m.pagerQuery)
			m.updatePagerMatches()
			return m, nil
		case tea.KeyRunes, tea.KeySpace:
			m.pagerQuery += string(msg.Runes)
			if msg.Type == tea.KeySpace {
				m.pagerQuery += " "
			}
			m.updatePagerMatches()
			return m, nil
		}
		return m, nil
	}

	pageH := m.pagerBodyHeight()
	maxScroll := len(m.pagerLines) - pageH
	if maxScroll < 0 {
		maxScroll = 0
	}

	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = modeNormal
		m.pagerQuery = ""
		m.pagerMatches = nil
		return m, nil
	case "j", "down":
		m.pagerScroll++
	case "k", "up":
		m.pagerScroll--
	case "pgdown", "ctrl+d", " ":
		m.pagerScroll += pageH
	case "pgup", "ctrl+u":
		m.pagerScroll -= pageH
	case "g", "home":
		m.pagerScroll = 0
	case "G", "end":
		m.pagerScroll = maxScroll
	case "/":
		m.pagerSearching = true
		m.pagerQuery = ""
		m.pagerMatches = nil
		m.pagerMatchIdx = 0
		return m, nil
	case "n":
		if len(m.pagerMatches) > 0 {
			m.pagerMatchIdx = (m.pagerMatchIdx + 1) % len(m.pagerMatches)
			m.pagerScroll = m.pagerMatches[m.pagerMatchIdx]
		}
	case "N":
		if len(m.pagerMatches) > 0 {
			m.pagerMatchIdx = (m.pagerMatchIdx - 1 + len(m.pagerMatches)) % len(m.pagerMatches)
			m.pagerScroll = m.pagerMatches[m.pagerMatchIdx]
		}
	case "y":
		text := strings.Join(m.pagerLines, "\n")
		return m, copyCmd(text, fmt.Sprintf("%d lines", len(m.pagerLines)))
	}

	if m.pagerScroll > maxScroll {
		m.pagerScroll = maxScroll
	}
	if m.pagerScroll < 0 {
		m.pagerScroll = 0
	}
	return m, nil
}

func (m *Model) updatePagerMatches() {
	m.pagerMatches = nil
	m.pagerMatchIdx = 0
	if m.pagerQuery == "" {
		return
	}
	q := strings.ToLower(m.pagerQuery)
	for i, line := range m.pagerLines {
		if strings.Contains(strings.ToLower(line), q) {
			m.pagerMatches = append(m.pagerMatches, i)
		}
	}
	if len(m.pagerMatches) > 0 {
		m.pagerScroll = m.pagerMatches[0]
		pageH := m.pagerBodyHeight()
		maxScroll := len(m.pagerLines) - pageH
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.pagerScroll > maxScroll {
			m.pagerScroll = maxScroll
		}
	}
}

// cycleFocus moves focus by delta positions through the shown panels, wrapping.
func (m Model) cycleFocus(delta int) (tea.Model, tea.Cmd) {
	list := m.panels()
	i := m.panelIndex(m.focus)
	if i < 0 {
		i = 0
	}
	m = m.setFocus(list[((i+delta)%len(list)+len(list))%len(list)])
	return m, m.updatePreview()
}

// focusPanelAt focuses the i'th shown panel, ignoring an out-of-range index so
// a "4" in compact mode does nothing rather than something surprising.
func (m Model) focusPanelAt(i int) (tea.Model, tea.Cmd) {
	list := m.panels()
	if i < 0 || i >= len(list) {
		return m, nil
	}
	m = m.setFocus(list[i])
	return m, m.updatePreview()
}

// pageSize is how far PgUp/PgDn move: one panel's worth of rows, less one for
// continuity. The exact panel height isn't known here, so use the share the
// layout would give it.
func (m Model) pageSize() int {
	n := (m.height - helpHeight) / int(numPanels)
	n -= borderSize + titleRows + 1
	if n < 1 {
		return 1
	}
	return n
}

// attachToSelection queues an attach to the current session, pre-selecting the
// focused window (and pane) so the client lands exactly where the cursor is.
func (m Model) attachToSelection() (tea.Model, tea.Cmd) {
	s, ok := m.currentSession()
	if !ok {
		return m, nil
	}
	m.pendingAttach = s.ID
	if w, ok := m.currentWindow(); ok {
		paneID := ""
		if p, ok := m.currentPane(); ok {
			paneID = p.ID
		}
		return m, tea.Sequence(selectTargetCmd(m.runner, w.ID, paneID), tea.Quit)
	}
	return m, tea.Quit
}

// startKill opens a confirm modal for the destructive kill appropriate to the
// focused panel. What that is — the action and the wording — comes from the
// panel table; this only puts the modal up.
func (m Model) startKill() (tea.Model, tea.Cmd) {
	f := m.focus.spec().kill
	if f == nil {
		return m, nil
	}
	pending, prompt, ok := f(m)
	if !ok {
		return m, nil
	}
	m.pending, m.confirmPrompt = pending, prompt
	m.mode = modeConfirm
	return m, nil
}

// startRename opens a text prompt to rename the focused session or window.
func (m Model) startRename() (tea.Model, tea.Cmd) {
	switch m.focus {
	case panelSessions:
		s, ok := m.currentSession()
		if !ok {
			return m, nil
		}
		m.pending = pendingAction{op: opRenameSession, sessionID: s.ID}
		return m.openPrompt("Rename session:", s.Name)
	case panelWindows:
		s, sok := m.currentSession()
		w, wok := m.currentWindow()
		if !sok || !wok {
			return m, nil
		}
		m.pending = pendingAction{op: opRenameWindow, sessionID: s.ID, windowID: w.ID}
		return m.openPrompt("Rename window:", w.Name)
	}
	return m, nil
}

// startNewWindow opens a text prompt for a new window's name in the current
// session.
func (m Model) startNewWindow() (tea.Model, tea.Cmd) {
	s, ok := m.currentSession()
	if !ok {
		return m, nil
	}
	m.pending = pendingAction{op: opNewWindow, sessionID: s.ID}
	return m.openPrompt("New window name:", "")
}

// startSplitPane opens a text prompt for an optional startup command in the new split pane.
func (m Model) startSplitPane(horizontal bool) (tea.Model, tea.Cmd) {
	w, wok := m.currentWindow()
	if !wok {
		return m, nil
	}
	targetPaneID := ""
	if p, pok := m.currentPane(); pok {
		targetPaneID = p.ID
	} else if len(m.panes) > 0 {
		targetPaneID = m.panes[0].ID
	}
	if targetPaneID == "" {
		return m, nil
	}

	op := opSplitPaneVertical
	title := "Split vertically (command, blank for shell):"
	if horizontal {
		op = opSplitPaneHorizontal
		title = "Split horizontally (command, blank for shell):"
	}
	m.pending = pendingAction{op: op, windowID: w.ID, paneID: targetPaneID}
	return m.openPrompt(title, "")
}

func (m Model) openPrompt(title, initial string) (tea.Model, tea.Cmd) {
	ti := textinput.New()
	// Without a Width the input has no scroll window, so a value longer than
	// the footer pushed the cursor off the right edge of the screen with no
	// way to see what was being typed.
	if w := m.width - lipgloss.Width(title) - 2; w > 8 {
		ti.Width = w
	} else {
		ti.Width = 8
	}
	ti.SetValue(initial)
	ti.CursorEnd()
	cmd := ti.Focus()
	m.textInput = ti
	m.promptTitle = title
	m.mode = modePrompt
	return m, cmd
}

// swapWindow swaps the focused window with the one delta positions away (+1 down, -1 up).
func (m Model) swapWindow(delta int) (tea.Model, tea.Cmd) {
	if m.focus != panelWindows {
		return m, nil
	}
	s, sok := m.currentSession()
	w, wok := m.currentWindow()
	if !sok || !wok {
		return m, nil
	}
	visible := m.visibleWindows()
	cur := m.cur[panelWindows]
	target := cur + delta
	if target < 0 || target >= len(visible) {
		return m, nil
	}
	dst := visible[target]
	m.cur[panelWindows] = target
	return m, swapWindowCmd(m.runner, s.ID, w.ID, dst.ID)
}

// startMoveWindow opens a session picker modal to transfer the current window to another session.
func (m Model) startMoveWindow() (tea.Model, tea.Cmd) {
	if m.focus != panelWindows {
		return m, nil
	}
	s, sok := m.currentSession()
	w, wok := m.currentWindow()
	if !sok || !wok {
		return m, nil
	}
	var targets []sessions.Session
	for _, sess := range m.sessions {
		if sess.ID != s.ID {
			targets = append(targets, sess)
		}
	}
	if len(targets) == 0 {
		m.info = "no other running sessions to move window to"
		return m, nil
	}
	m.mode = modeMoveWindow
	m.moveWindowSessions = targets
	m.moveWindowCur = 0
	m.moveWindowSrcID = w.ID
	m.moveWindowSrcSessionID = s.ID
	return m, nil
}

func (m Model) handleMoveWindowKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = modeNormal
		m.moveWindowSessions = nil
		return m, nil
	case "up", "k":
		if len(m.moveWindowSessions) > 0 {
			m.moveWindowCur = wrap(m.moveWindowCur-1, len(m.moveWindowSessions))
		}
		return m, nil
	case "down", "j":
		if len(m.moveWindowSessions) > 0 {
			m.moveWindowCur = wrap(m.moveWindowCur+1, len(m.moveWindowSessions))
		}
		return m, nil
	case "enter":
		if m.moveWindowCur >= 0 && m.moveWindowCur < len(m.moveWindowSessions) {
			target := m.moveWindowSessions[m.moveWindowCur]
			m.mode = modeNormal
			m.moveWindowSessions = nil
			return m, moveWindowCmd(m.runner, m.moveWindowSrcSessionID, m.moveWindowSrcID, target.ID)
		}
		m.mode = modeNormal
		m.moveWindowSessions = nil
		return m, nil
	}
	return m, nil
}

// cycleLayout advances the focused window through tmux's standard layouts.
func (m Model) cycleLayout() (tea.Model, tea.Cmd) {
	w, ok := m.currentWindow()
	if !ok {
		return m, nil
	}
	// Restart the cycle when the target window changes, so the first press on
	// a window always visibly changes something.
	if m.layoutWindow != w.ID {
		m.layoutWindow, m.layoutIdx = w.ID, -1
	}
	m.layoutIdx = (m.layoutIdx + 1) % len(cycleLayouts)
	return m, selectLayoutCmd(m.runner, w.ID, cycleLayouts[m.layoutIdx])
}

// zoomPane toggles zoom on the focused pane.
func (m Model) zoomPane() (tea.Model, tea.Cmd) {
	p, ok := m.currentPane()
	if !ok {
		return m, nil
	}
	return m, zoomPaneCmd(m.runner, p.ID)
}

// startProject builds-or-attaches the selected project's session and hands the
// terminal over (session.Create is idempotent, so this is start *and* attach).
func (m Model) startProject() (tea.Model, tea.Cmd) {
	p, ok := m.currentProject()
	if !ok {
		return m, nil
	}
	return m, startProjectCmd(m.runner, m.settings, p)
}

// editProject opens the selected project's config in $EDITOR.
func (m Model) editProject() (tea.Model, tea.Cmd) {
	p, ok := m.currentProject()
	if !ok {
		return m, nil
	}
	return m, editConfigCmd(m.runner, m.settings, p.Path)
}

func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	// Deliberately not "enter": enter is attach in normal mode, so accepting
	// it here turned a reflexive x-then-enter into a killed session.
	case "y", "Y":
		cmd := m.executePending()
		m.mode = modeNormal
		m.pending = pendingAction{}
		return m, cmd
	case "n", "N", "esc", "ctrl+c":
		m.mode = modeNormal
		m.pending = pendingAction{}
		return m, nil
	}
	return m, nil
}

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		value := m.textInput.Value()
		m.mode = modeNormal
		if value == "" && m.pending.op != opSplitPaneVertical && m.pending.op != opSplitPaneHorizontal {
			m.pending = pendingAction{}
			return m, nil
		}
		cmd := m.executePendingWithValue(value)
		m.pending = pendingAction{}
		return m, cmd
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeNormal
		m.pending = pendingAction{}
		return m, nil
	}
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// moveCursor moves the selection in the focused panel by delta and reloads the
// dependent panels/preview.
func (m Model) moveCursor(delta int) (tea.Model, tea.Cmd) {
	return m.moveCursorTo(m.cur[m.focus] + delta)
}

// moveCursorTo sets the focused panel's selection, clamping to the ends rather
// than refusing to move — a PgDn near the bottom should land on the last row,
// not do nothing — and reloads the panels below it.
func (m Model) moveCursorTo(next int) (tea.Model, tea.Cmd) {
	return m.setCursor(m.focus, next)
}

// setCursor is moveCursorTo for an arbitrary panel. The mouse needs it: a click
// or a wheel event names the panel it landed on, rather than acting on whatever
// currently has focus.
func (m Model) setCursor(p panel, next int) (tea.Model, tea.Cmd) {
	n := m.panelLen(p)
	if n == 0 {
		return m, nil
	}
	next = clampTo(next, n)
	if next == m.cur[p] {
		return m, nil
	}
	m.cur[p] = next
	return m, m.selectionChanged(p)
}

// selectionChanged resets the panels that hang off p and returns the command
// that reloads them.
//
// The cascade used to be spelled out per panel in both setCursor and
// clampFocused, four cases each. Resetting a child to -1 rather than 0 is what
// makes the reload snap to whatever tmux reports as active instead of to the
// first row. Which panel feeds which, and what to re-fetch, come from the panel
// table — see panels.go.
func (m *Model) selectionChanged(p panel) tea.Cmd {
	for child := p.spec().child; child != noPanel; child = child.spec().child {
		m.cur[child] = -1
	}
	if f := p.spec().reload; f != nil {
		return f(m)
	}
	return nil
}

// clampTo keeps an index inside [0, n).
func clampTo(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func (m *Model) reloadWindows() tea.Cmd {
	s, ok := m.currentSession()
	if !ok {
		m.windows, m.panes = nil, nil
		m.preview, m.previewTitle = "", ""
		return nil
	}
	return loadWindows(m.runner, s.ID)
}

func (m *Model) reloadPanes() tea.Cmd {
	w, ok := m.currentWindow()
	if !ok {
		m.panes = nil
		m.preview, m.previewTitle = "", ""
		return nil
	}
	return loadPanes(m.runner, w.ID)
}

// updatePreview points the main panel at the right source for the current
// focus: the selected project's config file (Projects panel) or the selected
// pane's live capture (everywhere else).
func (m *Model) updatePreview() tea.Cmd {
	if m.focus == panelProjects {
		m.previewSrc = previewConfig
		p, ok := m.currentProject()
		if !ok {
			m.preview, m.previewTitle = "", ""
			return nil
		}
		m.previewTitle = p.Path
		return loadConfigPreview(p.Path)
	}
	m.previewSrc = previewPane
	return m.reloadPreview()
}

func (m *Model) reloadPreview() tea.Cmd {
	p, ok := m.currentPane()
	if !ok {
		m.preview, m.previewTitle = "", ""
		return nil
	}
	// Never capture the pane wyrm tui itself occupies — that mirrors the TUI
	// into its own preview and thrashes on every refresh.
	if m.selfPane != "" && p.ID == m.selfPane {
		m.previewTitle = p.ID + " " + p.Command
		m.preview = selfPreviewNotice
		return nil
	}
	return loadPreview(m.runner, p.ID)
}

// Run drives the full four-panel session manager to completion and returns the
// tmux session ID to attach to (empty if the user quit without choosing). An
// error the model was still holding when the user quit is reported on stderr —
// the alt screen is gone by then, so the footer that would have shown it is too.
func Run(runner tmux.Runner, settings *config.Settings, stderr io.Writer) (pendingAttach string, err error) {
	return runProgram(New(runner, settings), settings, stderr)
}

// RunPicker drives the compact, fzf-style chooser behind `wyrm pick`: the same
// model and the same key map, with only the Sessions and Windows panels shown
// and the filter already open so typing narrows the list immediately.
//
// It is the same program as Run on purpose. `wyrm pick` used to be a second,
// independent implementation — a hand-rolled raw-mode terminal loop with its
// own escape-sequence decoder, its own frame renderer, its own signal handling
// and its own key names — offering a strict subset of what the TUI already did.
// Two key maps that disagreed about how to kill a session was the visible cost;
// the invisible one was that every new feature had to be built twice or quietly
// exist in only one of them.
func RunPicker(runner tmux.Runner, settings *config.Settings, stderr io.Writer) (pendingAttach string, err error) {
	m := New(runner, settings)
	m.compact = true
	m.focus = panelSessions
	// Straight into filter mode: `pick` exists to be typed at. The full TUI
	// starts in normal mode because it is a browser first and a finder second.
	m.mode, m.filtering = modeFilter, true
	return runProgram(m, settings, stderr)
}

func runProgram(m Model, settings *config.Settings, stderr io.Writer) (pendingAttach string, err error) {
	// Before the alt screen: a broken theme file or agent profile has to be
	// reportable, and the alt screen would wipe the message on its way up.
	theme, err := LoadTheme()
	if err != nil {
		return "", err
	}
	SetTheme(theme)
	if _, err := agentProfiles(settings); err != nil {
		return "", err
	}

	// ReportFocus so the refresh tickers can idle while the terminal is in a
	// background tab — see the tickMsg case in Update.
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithReportFocus()}
	// Cell motion, not all motion: the menu wants hover tracking while a button
	// is down, but reporting every idle pointer move would wake the program on
	// each pixel of travel for no benefit.
	if settings.MouseEnabled() {
		opts = append(opts, tea.WithMouseCellMotion())
	}

	fm, err := tea.NewProgram(m, opts...).Run()
	if err != nil {
		return "", err
	}
	final, ok := fm.(Model)
	if !ok {
		return "", fmt.Errorf("unexpected final model %T", fm)
	}
	if final.err != nil {
		_, _ = fmt.Fprintf(stderr, "wyrm: %v\n", final.err)
	}
	return final.pendingAttach, nil
}

// --- small helpers ---

// trimLastRune drops the last rune of s, not the last byte. A text-entry
// backspace handler that trims one byte leaves an invalid UTF-8 tail behind
// for any multi-byte character (e.g. "é"), which corrupts every comparison
// against that field afterward.
func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

// clamp keeps cur within [0, n); returns 0 when the list is empty.
func clamp(cur, n int) int {
	if n == 0 {
		return 0
	}
	if cur < 0 {
		return 0
	}
	if cur >= n {
		return n - 1
	}
	return cur
}

// activeOrClamp prefers the active window's index when the previous cursor is
// out of range (e.g. after first load), else keeps the cursor in bounds.
func activeOrClamp(cur int, windows []tmux.WindowInfo) int {
	if cur >= 0 && cur < len(windows) {
		return cur
	}
	for i, w := range windows {
		if w.Active {
			return i
		}
	}
	return clamp(cur, len(windows))
}

func activePaneOrClamp(cur int, panes []tmux.PaneInfo) int {
	if cur >= 0 && cur < len(panes) {
		return cur
	}
	for i, p := range panes {
		if p.Active {
			return i
		}
	}
	return clamp(cur, len(panes))
}
