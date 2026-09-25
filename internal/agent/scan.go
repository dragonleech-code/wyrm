package agent

import "github.com/jskoll/wyrm/internal/tmux"

// MaxCaptures bounds how many panes one scan will capture. Finding the
// candidates costs a single list-panes call, but reading each one costs a
// capture-pane, and both callers run on a timer: someone with a wall of agent
// panes should get a slightly incomplete picture rather than a tmux call storm
// every few seconds.
//
// The TUI has had this bound since it grew agent markers. `wyrm status` did
// not, so `wyrm status --watch` on a machine with 40 agent panes issued 40
// capture-pane calls every 2 seconds — the exact storm this constant exists to
// prevent, against a server the user is working in. Both callers now select
// candidates through Candidates so the bound cannot drift again.
const MaxCaptures = 16

// Candidates picks the agent panes worth capturing from refs, in list order,
// stopping at max. skipPane, when non-empty, is excluded: the pane the caller
// is itself rendering in is never worth capturing, and reading it is the
// mirror-of-a-mirror the TUI's preview avoids.
//
// skipped reports how many further agent panes were left unscanned, so a
// caller can say the picture is partial instead of quietly under-reporting.
// A limit of zero or less means no bound.
func Candidates(refs []tmux.PaneRef, profiles []Profile, skipPane string, limit int) (selected []tmux.PaneRef, skipped int) {
	for _, ref := range refs {
		if !IsAgentPane(ref.Command, profiles) {
			continue
		}
		if skipPane != "" && ref.PaneID == skipPane {
			continue
		}
		if limit > 0 && len(selected) >= limit {
			skipped++
			continue
		}
		selected = append(selected, ref)
	}
	return selected, skipped
}

// Detection pairs a captured pane with its recognized state.
type Detection struct {
	Ref   tmux.PaneRef
	State State
}

// Scan applies the same capture and classification policy to status and the TUI.
func Scan(r tmux.Runner, refs []tmux.PaneRef, profiles []Profile, skipPane string, limit int) ([]Detection, int) {
	candidates, skipped := Candidates(refs, profiles, skipPane, limit)
	cmds := make([][]string, len(candidates))
	for i, ref := range candidates {
		cmds[i] = tmux.CapturePanePlainArgs(ref.PaneID)
	}
	contents := tmux.RunOutputs(r, cmds)
	var detected []Detection
	for i, ref := range candidates {
		if i >= len(contents) || contents[i] == "" {
			continue
		}
		state := Detect(ref.Command, contents[i], profiles)
		if state != StateNone && state != StateUnknown {
			detected = append(detected, Detection{Ref: ref, State: state})
		}
	}
	return detected, skipped
}
