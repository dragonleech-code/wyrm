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
	delivery := func() tea.Msg { _ = agent.Dispatch(n, cfg, nil); return nil }
	if terminal.Len() == 0 {
		return delivery
	}
	return tea.Sequence(tea.Exec(&terminalOutput{text: terminal.String()}, nil), delivery)
}

type terminalOutput struct {
	text string
	out  io.Writer
}

func (c *terminalOutput) SetStdin(io.Reader)    {}
func (c *terminalOutput) SetStderr(io.Writer)   {}
func (c *terminalOutput) SetStdout(w io.Writer) { c.out = w }
func (c *terminalOutput) Run() error            { _, err := io.WriteString(c.out, c.text); return err }
