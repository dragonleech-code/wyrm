package tui

import tea "github.com/charmbracelet/bubbletea"

// This file is the one description of what a panel *is*.
//
// Every panel-shaped question — how many rows does it have, what does it draw,
// what does its footer say, what does its context menu offer, what does "x" do
// on it — used to be its own four-case switch, and there were nine of them
// spread across four files. Adding a panel meant finding all nine; forgetting
// one produced a panel that looked right and behaved wrong in a corner.
//
// The switches are now this table. What stays outside it is anything that
// genuinely differs in *type* rather than in behaviour: the four list slices on
// Model, and the four visible* accessors that filter them, because they hold
// Projects, Sessions, WindowInfos and PaneInfos respectively and no useful
// common interface exists over them.

// panelSpec describes one panel.
type panelSpec struct {
	// title is the box heading, and empty the hint shown in place of rows when
	// there are none. An empty `empty` means the panel simply renders blank —
	// Windows and Panes are meaningless without a session selected, and saying
	// so twice adds nothing.
	title string
	empty string

	// length is the number of rows currently visible, filter applied. It is
	// separate from rows because the layout asks for it on every render and on
	// every mouse event, and building the styled spans just to count them would
	// be wasteful.
	length func(Model) int

	// rows builds the visible rows as styled spans.
	rows func(Model) [][]span

	// keys is the contextual footer shown while this panel has focus.
	keys string

	// menu is the right-click/M context menu for the current selection, or nil
	// when there is nothing to act on.
	menu func(Model) []menuEntry

	// kill describes what "x" destroys here: the pending action and the text of
	// the confirm prompt. ok is false when there is nothing to kill.
	kill func(Model) (pendingAction, string, bool)

	// child is the panel whose contents depend on this one's selection, or
	// noPanel. Projects deliberately has none: it is a sibling list of
	// *configs*, not the parent of the running Sessions — moving it changes the
	// preview and nothing else, and must not disturb the session the user has
	// selected. Inferring the cascade from panel order gets that wrong, so the
	// relationship is written down.
	child panel

	// reload is what to re-fetch once this panel's selection moves. Unlike the
	// rest of this struct it takes a *Model, because it is the one entry that
	// mutates — the others are pure reads.
	reload func(*Model) tea.Cmd
}

// navKeys is the part of the footer that never changes.
const navKeys = "tab/1-4: focus  jk: move  /: filter  ^f/^z: search panel/all  R: reload  ?: help  q: quit"

// panelSpecs is indexed by panel.
var panelSpecs = [numPanels]panelSpec{
	panelProjects: {
		title:  "Projects",
		empty:  "no wyrm configs found",
		length: func(m Model) int { return len(m.visibleProjects()) },
		rows:   projectRows,
		keys:   "↵: start/attach  e: edit  x: stop  " + navKeys,
		menu:   projectMenu,
		kill:   killProject,
		child:  noPanel,
		reload: (*Model).updatePreview,
	},
	panelSessions: {
		title:  "Sessions",
		empty:  "no running sessions",
		length: func(m Model) int { return len(m.visibleSessions()) },
		rows:   sessionRows,
		keys:   "↵: attach/start  x: kill  r: rename  n: new-win  a: all  " + navKeys,
		menu:   sessionMenu,
		kill:   killSession,
		child:  panelWindows,
		reload: (*Model).reloadWindows,
	},
	panelWindows: {
		title:  "Windows",
		length: func(m Model) int { return len(m.visibleWindows()) },
		rows:   windowRows,
		keys:   "↵: attach  x: kill  r: rename  n: new-win  </>: reorder  W: move-to  s/S: split  L: layout  " + navKeys,
		menu:   windowMenu,
		kill:   killWindow,
		child:  panelPanes,
		reload: (*Model).reloadPanes,
	},
	panelPanes: {
		title:  "Panes",
		length: func(m Model) int { return len(m.visiblePanes()) },
		rows:   paneRows,
		keys:   "↵: attach  x: kill  s/S: split  z: zoom  " + navKeys,
		menu:   paneMenu,
		kill:   killPane,
		child:  noPanel,
		reload: (*Model).reloadPreview,
	},
}

// spec returns p's description. An out-of-range panel yields the zero spec,
// whose funcs are nil — callers guard, rather than this panicking on a value
// that can only come from a bug.
func (p panel) spec() panelSpec {
	if p < 0 || p >= numPanels {
		return panelSpec{}
	}
	return panelSpecs[p]
}

// panelLen is the number of rows currently displayed in a panel.
func (m Model) panelLen(p panel) int {
	if f := p.spec().length; f != nil {
		return f(m)
	}
	return 0
}

// --- kill descriptions ---

func killProject(m Model) (pendingAction, string, bool) {
	p, ok := m.currentProject()
	if !ok || !p.Running {
		return pendingAction{}, "", false
	}
	return pendingAction{op: opKillProject, path: p.Path, root: p.Root, wildcard: p.Wildcard},
		"Stop project '" + p.Name + "' (runs on_project_exit)?  (y/n)", true
}

func killSession(m Model) (pendingAction, string, bool) {
	s, ok := m.currentSession()
	if !ok {
		return pendingAction{}, "", false
	}
	return pendingAction{op: opKillSession, sessionID: s.ID},
		"Kill session '" + s.Name + "'?  (y/n)", true
}

func killWindow(m Model) (pendingAction, string, bool) {
	s, sok := m.currentSession()
	w, wok := m.currentWindow()
	if !sok || !wok {
		return pendingAction{}, "", false
	}
	return pendingAction{op: opKillWindow, sessionID: s.ID, windowID: w.ID},
		"Kill window '" + w.Name + "'?  (y/n)", true
}

func killPane(m Model) (pendingAction, string, bool) {
	w, wok := m.currentWindow()
	p, pok := m.currentPane()
	if !wok || !pok {
		return pendingAction{}, "", false
	}
	return pendingAction{op: opKillPane, windowID: w.ID, paneID: p.ID},
		"Kill pane " + p.ID + " (" + p.Command + ")?  (y/n)", true
}
