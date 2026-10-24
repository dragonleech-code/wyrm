package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/sessions"
	"github.com/jskoll/wyrm/internal/tmux"
)

// actionOp identifies a pending management action awaiting confirmation (kills)
// or text input (rename/new-window). Storing the op + its target IDs as plain
// data — rather than a closure — keeps Model comparable and easy to unit-test.
type actionOp int

const (
	opNone actionOp = iota
	opKillSession
	opKillWindow
	opKillPane
	opRenameSession
	opRenameWindow
	opNewWindow
	opKillProject
	opSplitPaneVertical
	opSplitPaneHorizontal
	opApproveAgent
	opTrustAndStart
)

// pendingAction captures what a modal will do once confirmed/submitted, along
// with the tmux IDs it needs. The relevant subset of IDs is filled per op.
//
// The opKillProject fields spell a project's identity out as scalars rather
// than holding a config.Project, whose Aliases slice would make Model
// non-comparable. Path alone is not enough: a wildcard project's path is the
// shared template and a zoxide directory has none, so root/wildcard have to
// travel with it — see config.Project.LoadConfig.
type pendingAction struct {
	op        actionOp
	sessionID string
	windowID  string
	paneID    string

	path     string // config path, for opKillProject ("" for a fileless project)
	root     string // the project's directory, for opKillProject
	wildcard bool   // whether path is a shared wildcard template

	command     string // the pane's foreground command, for opApproveAgent
	fingerprint string // the config's fingerprint when it was shown, for opTrustAndStart
}

// project rebuilds the identity opKillProject was stored with.
func (p pendingAction) project() config.Project {
	return config.Project{Path: p.path, Root: p.root, Wildcard: p.wildcard}
}

// actionErrMsg reports that a management action failed; Update stores it in
// m.err for display. Successful actions instead return a reload message
// (sessionsMsg/windowsMsg/panesMsg) so the existing handlers refresh the view.
type actionErrMsg struct{ err error }

// tmux layouts cycled by the "L" key on the Windows panel, in tmux's own order.
var cycleLayouts = []string{"even-horizontal", "even-vertical", "main-horizontal", "main-vertical", "tiled"}

// --- action commands: each mutates via tmux, then re-lists the affected level
// and returns the matching load message so Update folds the fresh data in. ---

func killSessionCmd(r tmux.Runner, sessionID string) tea.Cmd {
	return func() tea.Msg {
		if err := sessions.Kill(r, sessionID); err != nil {
			return actionErrMsg{err}
		}
		s, err := sessions.List(r)
		return sessionsMsg{sessions: s, err: err}
	}
}

func killWindowCmd(r tmux.Runner, sessionID, windowID string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.KillWindow(r, windowID); err != nil {
			return actionErrMsg{err}
		}
		w, err := tmux.ListWindows(r, sessionID)
		return windowsMsg{sessionID: sessionID, windows: w, err: err}
	}
}

func killPaneCmd(r tmux.Runner, windowID, paneID string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.KillPane(r, paneID); err != nil {
			return actionErrMsg{err}
		}
		p, err := tmux.ListPanes(r, windowID)
		return panesMsg{windowID: windowID, panes: p, err: err}
	}
}

func renameSessionCmd(r tmux.Runner, sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.RenameSession(r, sessionID, name); err != nil {
			return actionErrMsg{err}
		}
		s, err := sessions.List(r)
		return sessionsMsg{sessions: s, err: err}
	}
}

func renameWindowCmd(r tmux.Runner, sessionID, windowID, name string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.RenameWindow(r, windowID, name); err != nil {
			return actionErrMsg{err}
		}
		w, err := tmux.ListWindows(r, sessionID)
		return windowsMsg{sessionID: sessionID, windows: w, err: err}
	}
}

func newWindowCmd(r tmux.Runner, sessionID, name string) tea.Cmd {
	return func() tea.Msg {
		if _, _, err := tmux.NewWindow(r, sessionID, name, ""); err != nil {
			return actionErrMsg{err}
		}
		w, err := tmux.ListWindows(r, sessionID)
		return windowsMsg{sessionID: sessionID, windows: w, err: err}
	}
}

func swapWindowCmd(r tmux.Runner, sessionID, srcWindowID, dstWindowID string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.SwapWindow(r, srcWindowID, dstWindowID); err != nil {
			return actionErrMsg{err}
		}
		w, err := tmux.ListWindows(r, sessionID)
		return windowsMsg{sessionID: sessionID, windows: w, err: err}
	}
}

func moveWindowCmd(r tmux.Runner, srcSessionID, windowID, dstSessionID string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.MoveWindow(r, windowID, dstSessionID); err != nil {
			return actionErrMsg{err}
		}
		w, err := tmux.ListWindows(r, srcSessionID)
		return windowsMsg{sessionID: srcSessionID, windows: w, err: err}
	}
}

func splitPaneCmd(r tmux.Runner, windowID, paneID string, horizontal bool, command string) tea.Cmd {
	return func() tea.Msg {
		if _, err := tmux.SplitPane(r, paneID, horizontal, command, ""); err != nil {
			return actionErrMsg{err}
		}
		p, err := tmux.ListPanes(r, windowID)
		return panesMsg{windowID: windowID, panes: p, err: err}
	}
}

func selectLayoutCmd(r tmux.Runner, windowID, layout string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.SelectLayout(r, windowID, layout); err != nil {
			return actionErrMsg{err}
		}
		p, err := tmux.ListPanes(r, windowID)
		return panesMsg{windowID: windowID, panes: p, err: err}
	}
}

func zoomPaneCmd(r tmux.Runner, paneID string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.ZoomPane(r, paneID); err != nil {
			return actionErrMsg{err}
		}
		return nil
	}
}

// selectTargetCmd pre-selects the window (and pane) an attach should land on.
// Sequenced before tea.Quit, so a failure here — a stale target killed
// elsewhere between listing and attaching, say — has to surface as an
// actionErrMsg rather than being discarded: by the time Quit runs there is no
// later chance to tell the user the client landed somewhere other than the
// row they actually picked. runProgram prints m.err to stderr after the
// program exits for exactly this kind of failure.
func selectTargetCmd(r tmux.Runner, windowID, paneID string) tea.Cmd {
	return func() tea.Msg {
		if err := tmux.SelectWindow(r, windowID); err != nil {
			return actionErrMsg{fmt.Errorf("selecting window %s: %w", windowID, err)}
		}
		if paneID != "" {
			if err := tmux.SelectPane(r, paneID); err != nil {
				return actionErrMsg{fmt.Errorf("selecting pane %s: %w", paneID, err)}
			}
		}
		return nil
	}
}

// executePending builds the command for a confirmed kill action.
func (m Model) executePending() tea.Cmd {
	switch m.pending.op {
	case opKillSession:
		return killSessionCmd(m.runner, m.pending.sessionID)
	case opKillWindow:
		return killWindowCmd(m.runner, m.pending.sessionID, m.pending.windowID)
	case opKillPane:
		return killPaneCmd(m.runner, m.pending.windowID, m.pending.paneID)
	case opKillProject:
		return killProjectCmd(m.runner, m.settings, m.pending.project())
	case opApproveAgent:
		return approveAgentCmd(m.runner, m.pending.paneID, agent.ApproveKeys(m.pending.command, m.agentProfiles))
	case opTrustAndStart:
		return trustAndStartCmd(m.runner, m.settings, m.pending.project(), m.pending.fingerprint)
	}
	return nil
}

// executePendingWithValue builds the command for a submitted text action.
func (m Model) executePendingWithValue(value string) tea.Cmd {
	switch m.pending.op {
	case opRenameSession:
		return renameSessionCmd(m.runner, m.pending.sessionID, value)
	case opRenameWindow:
		return renameWindowCmd(m.runner, m.pending.sessionID, m.pending.windowID, value)
	case opNewWindow:
		return newWindowCmd(m.runner, m.pending.sessionID, value)
	case opSplitPaneVertical:
		return splitPaneCmd(m.runner, m.pending.windowID, m.pending.paneID, false, value)
	case opSplitPaneHorizontal:
		return splitPaneCmd(m.runner, m.pending.windowID, m.pending.paneID, true, value)
	}
	return nil
}
