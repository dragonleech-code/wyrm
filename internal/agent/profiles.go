package agent

import (
	"fmt"

	"github.com/jskoll/wyrm/internal/config"
)

// ProfilesFrom builds the detector's profiles from the user's settings, and
// reports what is wrong with them rather than quietly falling back — a
// mistyped busy_pattern that silently disabled the markers looks exactly like
// an agent that never waits for you.
//
// Layering: an explicit [[tui.agent.profiles]] list replaces the built-in
// profile outright, because a user who has described their own agents has said
// what wyrm should look for, and folding the shipped Claude patterns back in
// would let one agent's chrome decide another's state. A bare `commands` list
// instead widens the built-in profile, which is what someone running Claude
// Code under a wrapper name wants.
//
// This lives here, rather than next to either caller, because there are three
// of them — the TUI's marker scan, `wyrm status`, and `wyrm doctor` — and two
// had already grown their own copy of it. Importing internal/config from this
// package is safe and deliberate: the dependency the AgentProfile comment
// guards against is the other direction, config depending on the detector.
func ProfilesFrom(settings *config.Settings) ([]Profile, error) {
	configured := settings.AgentProfiles()
	if len(configured) == 0 {
		def := DefaultProfile()
		if extra := settings.AgentCommands(); len(extra) > 0 {
			def.Commands = extra
		}
		return []Profile{def}, nil
	}
	out := make([]Profile, 0, len(configured))
	for i, p := range configured {
		compiled, err := Profile{
			Commands:    p.Commands,
			Busy:        p.Busy,
			Blocked:     p.Blocked,
			Idle:        p.Idle,
			BusyPattern: p.BusyPattern,
			Approve:     p.Approve,
		}.Compile()
		if err != nil {
			return nil, fmt.Errorf("tui.agent.profiles[%d]: %w", i, err)
		}
		out = append(out, compiled)
	}
	return out, nil
}
