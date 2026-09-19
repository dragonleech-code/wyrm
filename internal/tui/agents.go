package tui

import (
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
		candidates, _ := agent.Candidates(refs, profiles, skipPane, agent.MaxCaptures)
		if len(candidates) == 0 {
			return agentStatusMsg{status: status}
		}

		cmds := make([][]string, len(candidates))
		for i, ref := range candidates {
			cmds[i] = tmux.CapturePanePlainArgs(ref.PaneID)
		}
		// A pane that died between listing and capturing is ordinary, and it
		// stops a batch short — so the ones it cut off are read individually
		// rather than lost. A pane that could not be read comes back empty and,
		// like a pane with nothing on it, simply stays unmarked.
		contents := tmux.RunOutputs(r, cmds)

		for i, ref := range candidates {
			if i >= len(contents) || contents[i] == "" {
				continue
			}
			state := agent.Detect(ref.Command, contents[i], profiles)
			// Unknown is stored nowhere: it draws no marker and must not win a
			// rollup, and leaving it out keeps the maps to panes worth showing.
			if state == agent.StateNone || state == agent.StateUnknown {
				continue
			}
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
// this only chooses the glyph. The two used to be stated separately — a
// predicate in the domain package and a switch here — which is two places to
// remember when a state is added.
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
