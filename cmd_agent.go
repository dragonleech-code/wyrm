package main

// `wyrm agent`: act on the agents `wyrm status` reports — jump to the next one
// waiting for you, or answer its prompt without switching to it.

import (
	"fmt"
	"os"
	"strings"

	"github.com/jskoll/wyrm/internal/agent"
	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/tmux"
)

func (a *app) agentCmd(args []string) error {
	if len(args) == 0 {
		return usageErrf("wyrm agent needs an action: next or approve")
	}
	switch args[0] {
	case "next":
		return a.agentNext(args[1:])
	case "approve":
		return a.agentApprove(args[1:])
	}
	return usageErrf("unknown agent action %q (want next or approve)", args[0])
}

func (a *app) agentProfiles() ([]agent.Profile, error) {
	settings, err := config.LoadSettings()
	if err != nil {
		return nil, err
	}
	return agent.ProfilesFrom(settings)
}

// agentNext switches to the next agent pane in the given state, after the
// pane wyrm is running in (so repeated presses of a tmux binding cycle
// through them), wrapping around.
func (a *app) agentNext(args []string) error {
	fs := a.newFlagSet("agent")
	want := fs.String("state", "blocked", "which agents to visit: blocked, idle, or any")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	switch *want {
	case "blocked", "idle", "any":
	default:
		return usageErrf("-state must be blocked, idle, or any, got %q", *want)
	}
	profiles, err := a.agentProfiles()
	if err != nil {
		return err
	}
	report, err := collectStatus(a.runner, "", profiles)
	if err != nil {
		return err
	}
	var matches []agentStatusPane
	for _, ag := range report.Agents {
		if *want == "any" || ag.State == *want {
			matches = append(matches, ag)
		}
	}
	if len(matches) == 0 {
		_, _ = fmt.Fprintf(a.stdout, "no %s agents\n", *want)
		return nil
	}
	target := nextAfter(matches, os.Getenv("TMUX_PANE"))
	if out, err := a.runner.Run("select-window", "-t", target.WindowID); err != nil {
		return fmt.Errorf("selecting window: %w", tmux.CmdErr(err, out))
	}
	if out, err := a.runner.Run("select-pane", "-t", target.PaneID); err != nil {
		return fmt.Errorf("selecting pane: %w", tmux.CmdErr(err, out))
	}
	_, _ = fmt.Fprintf(a.stdout, "%s: %s %s (%s)\n", target.SessionName, target.WindowName, target.PaneID, target.State)
	return a.attachOrSwitch(target.SessionID)
}

// nextAfter returns the agent after current in list order, wrapping; the first
// one when current is not an agent pane.
func nextAfter(agents []agentStatusPane, current string) agentStatusPane {
	for i, ag := range agents {
		if ag.PaneID == current {
			return agents[(i+1)%len(agents)]
		}
	}
	return agents[0]
}

// agentApprove sends a blocked agent the keys that accept its prompt. With no
// target it acts only when exactly one agent is blocked; answering a prompt
// you haven't chosen is not something to guess at.
func (a *app) agentApprove(args []string) error {
	fs := a.newFlagSet("agent")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usageErrf("approve takes at most one target: a pane ID, or a session name or ID")
	}
	target := fs.Arg(0)
	profiles, err := a.agentProfiles()
	if err != nil {
		return err
	}
	report, err := collectStatus(a.runner, "", profiles)
	if err != nil {
		return err
	}
	var blocked []agentStatusPane
	for _, ag := range report.Agents {
		if ag.State != agent.StateBlocked.String() {
			continue
		}
		if target == "" || ag.PaneID == target || ag.SessionName == target || ag.SessionID == target {
			blocked = append(blocked, ag)
		}
	}
	switch len(blocked) {
	case 0:
		if target != "" {
			return fmt.Errorf("no blocked agent matches %q", target)
		}
		return fmt.Errorf("no blocked agents")
	case 1:
	default:
		var names []string
		for _, ag := range blocked {
			names = append(names, fmt.Sprintf("%s (%s: %s)", ag.PaneID, ag.SessionName, ag.WindowName))
		}
		return fmt.Errorf("%d agents are blocked; name one: %s", len(blocked), strings.Join(names, ", "))
	}
	ag := blocked[0]
	for _, key := range agent.ApproveKeys(ag.Command, profiles) {
		if out, err := a.runner.Run("send-keys", "-t", ag.PaneID, key); err != nil {
			return fmt.Errorf("sending %s to %s: %w", key, ag.PaneID, tmux.CmdErr(err, out))
		}
	}
	_, _ = fmt.Fprintf(a.stdout, "approved %s (%s: %s)\n", ag.PaneID, ag.SessionName, ag.WindowName)
	return nil
}
