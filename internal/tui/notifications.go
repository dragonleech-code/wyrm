package tui

import (
	"bytes"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jskoll/wyrm/internal/agent"
)

// Hand terminal output to Bubble Tea so its renderer is paused during writes.
func notificationCmd(n agent.Notification, cfg agent.NotifyConfig) tea.Cmd {
	if !cfg.Enabled || (n.State == agent.StateBlocked && !cfg.OnBlocked) || (n.State == agent.StateIdle && !cfg.OnIdle) {
		return nil
	}
	var terminal bytes.Buffer
	if cfg.Bell {
		terminal.WriteByte('\a')
	}
	if cfg.OSC {
		agent.EmitTerminalNotification(&terminal, n.FormattedTitle(), n.FormattedMessage())
	}
	delivery := func() tea.Msg {
		if err := agent.Dispatch(n, cfg, nil); err != nil {
			return notificationErrorMsg{err: err}
		}
		return nil
	}
	if terminal.Len() == 0 {
		return delivery
	}
	return tea.Sequence(tea.Exec(&terminalOutput{text: terminal.String()}, nil), delivery)
}

type notificationErrorMsg struct{ err error }

func (m Model) agentTransitionCommands(status agentStatus) []tea.Cmd {
	if m.settings == nil || !m.settings.AgentNotifyEnabled() || m.prevPaneStates == nil {
		return nil
	}
	cfg := agent.NotifyConfig{
		Enabled: true, Desktop: m.settings.AgentNotifyDesktop(),
		Bell: m.settings.AgentNotifyBell(), OSC: m.settings.AgentNotifyOSC(),
		OnBlocked: m.settings.AgentNotifyOnBlocked(), OnIdle: m.settings.AgentNotifyOnIdle(),
		Command: m.settings.AgentNotifyCommand(),
	}
	var commands []tea.Cmd
	for paneID, state := range status.panes {
		if state == m.prevPaneStates[paneID] || (state != agent.StateBlocked && state != agent.StateIdle) {
			continue
		}
		ref := status.refs[paneID]
		commands = append(commands, notificationCmd(agent.Notification{
			State: state, PaneID: paneID, SessionName: ref.SessionName, WindowName: ref.WindowName,
		}, cfg))
	}
	return commands
}

type terminalOutput struct {
	text string
	out  io.Writer
}

func (c *terminalOutput) SetStdin(io.Reader)    {}
func (c *terminalOutput) SetStderr(io.Writer)   {}
func (c *terminalOutput) SetStdout(w io.Writer) { c.out = w }
func (c *terminalOutput) Run() error            { _, err := io.WriteString(c.out, c.text); return err }
