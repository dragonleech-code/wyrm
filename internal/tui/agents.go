package tui

import (
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

// agentStatus holds the detected state of every agent pane, rolled up to the
// windows and sessions that contain them so all four panels can be marked from
// one pass. The maps are keyed by tmux ID and are nil until the first scan
// lands — a nil map reads as StateNone, which is the right answer before
// anything is known.
type agentStatus struct {
	panes    map[string]agent.State
	windows  map[string]agent.State
	sessions map[string]agent.State
	refs     map[string]tmux.PaneRef
}

func (a agentStatus) pane(id string) agent.State    { return a.panes[id] }
func (a agentStatus) window(id string) agent.State  { return a.windows[id] }
func (a agentStatus) session(id string) agent.State { return a.sessions[id] }

// agentStatusMsg carries a completed scan. An error is deliberately not
// reported to the footer: this is a background poll of an optional decoration,
// and a transient tmux hiccup shouldn't stamp on an error the user was reading.
// A failed scan just leaves the previous markers in place.
type agentStatusMsg struct {
	status agentStatus
	err    error
}

// loadAgentStatus scans the whole server for agent panes and classifies each.
//
// It takes one list-panes call plus one capture-pane per agent pane — which is
// why the candidate filter happens before any capture, and why a pane running
// an ordinary shell is never captured at all.
func loadAgentStatus(r tmux.Runner, profiles []agent.Profile, skipPane string) tea.Cmd {
	return func() tea.Msg {
		refs, err := tmux.ListAllPanes(r)
		if err != nil {
			return agentStatusMsg{err: err}
		}
		status := agentStatus{
			panes:    map[string]agent.State{},
			windows:  map[string]agent.State{},
			sessions: map[string]agent.State{},
			refs:     map[string]tmux.PaneRef{},
		}
		for _, ref := range refs {
			status.refs[ref.PaneID] = ref
		}
		// Pick the panes worth reading first, then read them all at once. The
		// captures are independent of one another, so on a Runner that batches
		// this is a single tmux process instead of up to agent.MaxCaptures of
		// them, every listRefreshInterval, for as long as the TUI is open.
		detections, _ := agent.Scan(r, refs, profiles, skipPane, agent.MaxCaptures)
		for _, detection := range detections {
			ref, state := detection.Ref, detection.State
			status.panes[ref.PaneID] = state
			status.windows[ref.WindowID] = agent.Merge(status.windows[ref.WindowID], state)
			status.sessions[ref.SessionID] = agent.Merge(status.sessions[ref.SessionID], state)
		}
		return agentStatusMsg{status: status}
	}
}

// agentCmd returns the scan command, or nil when detection is switched off.
func (m Model) agentCmd() tea.Cmd {
	if !m.settings.AgentEnabled() {
		return nil
	}
	return loadAgentStatus(m.runner, m.agentProfiles, m.selfPane)
}

// agentProfiles builds the detector's profiles from settings. The rules live
// in agent.ProfilesFrom so the TUI, `wyrm status`, and `wyrm doctor` cannot
// disagree about which panes are agents or what marks them busy.
func agentProfiles(settings *config.Settings) ([]agent.Profile, error) {
	return agent.ProfilesFrom(settings)
}

// agentMark returns the trailing status span for a row, and whether there is
// one.
//
// agent.State.NeedsUser is the single definition of which states earn a marker;
// this only chooses the glyph.
func agentMark(state agent.State) (span, bool) {
	if !state.NeedsUser() {
		return span{}, false
	}
	if state == agent.StateBlocked {
		return span{blockedMark, " " + blockedGlyph}, true
	}
	return span{idleMark, " " + idleGlyph}, true
}

// The markers. Two glyphs rather than one because the two states want different
// things from the user: "⏸" is an agent stopped on a question it can't answer
// itself, "✓" one that finished and is waiting for the next instruction.
const (
	blockedGlyph = "⏸"
	idleGlyph    = "✓"
)

// blocked returns every pane the last scan found blocked, in session, window,
// pane order, so jumping through them visits them the way the panels list
// them.
func (a agentStatus) blocked() []tmux.PaneRef {
	var out []tmux.PaneRef
	for id, state := range a.panes {
		if state == agent.StateBlocked {
			if ref, ok := a.refs[id]; ok {
				out = append(out, ref)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.SessionName != y.SessionName {
			return x.SessionName < y.SessionName
		}
		if x.WindowIndex != y.WindowIndex {
			return x.WindowIndex < y.WindowIndex
		}
		return x.PaneIndex < y.PaneIndex
	})
	return out
}

// jumpToBlockedAgent attaches to the next blocked agent after the selected
// pane, wrapping — the TUI's counterpart of `wyrm agent next`.
func (m Model) jumpToBlockedAgent() (tea.Model, tea.Cmd) {
	blocked := m.agents.blocked()
	if len(blocked) == 0 {
		m.info = "no agents are waiting on a prompt"
		return m, nil
	}
	next := blocked[0]
	if p, ok := m.currentPane(); ok {
		for i, ref := range blocked {
			if ref.PaneID == p.ID {
				next = blocked[(i+1)%len(blocked)]
				break
			}
		}
	}
	m.pendingAttach = next.SessionID
	return m, tea.Sequence(selectTargetCmd(m.runner, next.WindowID, next.PaneID), tea.Quit)
}

// startApproveAgent asks before answering the selected pane's prompt. Only a
// pane the scan saw blocked qualifies: sending Enter to anything else is just
// typing into it.
func (m Model) startApproveAgent() (tea.Model, tea.Cmd) {
	p, ok := m.currentPane()
	if !ok {
		return m, nil
	}
	if m.agents.pane(p.ID) != agent.StateBlocked {
		m.info = p.ID + " isn't waiting on a prompt"
		return m, nil
	}
	m.pending = pendingAction{op: opApproveAgent, paneID: p.ID, command: p.Command}
	m.confirmPrompt = fmt.Sprintf("Approve the prompt in %s (%s)?  (y/n)", p.ID, p.Command)
	m.mode = modeConfirm
	return m, nil
}

// agentApprovedMsg reports a sent approval, so Update can say so and rescan.
type agentApprovedMsg struct{ paneID string }

func approveAgentCmd(r tmux.Runner, paneID string, keys []string) tea.Cmd {
	return func() tea.Msg {
		for _, key := range keys {
			if out, err := r.Run("send-keys", "-t", paneID, key); err != nil {
				return actionErrMsg{fmt.Errorf("approving %s: %w", paneID, tmux.CmdErr(err, out))}
			}
		}
		return agentApprovedMsg{paneID: paneID}
	}
}
