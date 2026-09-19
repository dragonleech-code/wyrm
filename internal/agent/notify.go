package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/jskoll/wyrm/internal/process"
)

// Notification holds the data for an agent state transition alert.
type Notification struct {
	Title       string
	Message     string
	State       State
	PaneID      string
	SessionName string
	WindowName  string
}

// NotifyConfig defines how notifications are delivered.
type NotifyConfig struct {
	Enabled   bool
	Desktop   bool
	Bell      bool
	OSC       bool
	OnBlocked bool
	OnIdle    bool
	Command   string
}

// FormattedTitle returns a standard title for the notification.
func (n Notification) FormattedTitle() string {
	if n.Title != "" {
		return n.Title
	}
	if n.SessionName != "" {
		return fmt.Sprintf("Wyrm Agent: %s", n.SessionName)
	}
	return "Wyrm Agent Alert"
}

// FormattedMessage returns a standard message body for the notification.
func (n Notification) FormattedMessage() string {
	if n.Message != "" {
		return n.Message
	}
	target := n.PaneID
	if n.WindowName != "" {
		target = fmt.Sprintf("%s (%s)", n.WindowName, n.PaneID)
	}
	switch n.State {
	case StateBlocked:
		return fmt.Sprintf("Agent in %s needs confirmation or input", target)
	case StateIdle:
		return fmt.Sprintf("Agent in %s has finished its turn", target)
	default:
		return fmt.Sprintf("Agent in %s changed state to %s", target, n.State)
	}
}

// BuildCustomNotifyCommand builds an *exec.Cmd for custom notification command execution,
// passing notification fields via environment variables instead of string interpolation
// to prevent shell command injection.
func BuildCustomNotifyCommand(command string, n Notification, title, msg string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.Env = append(os.Environ(),
		"WYRM_NOTIFY_TITLE="+title,
		"WYRM_NOTIFY_MESSAGE="+msg,
		"WYRM_NOTIFY_STATE="+n.State.String(),
		"WYRM_NOTIFY_SESSION="+n.SessionName,
		"WYRM_NOTIFY_PANE="+n.PaneID,
	)
	return cmd
}

// SanitizeTerminalString strips ASCII control characters (bytes < 0x20) and DEL (0x7f)
// to prevent terminal escape sequence injection and control character abuse.
func SanitizeTerminalString(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 0x20 && b != 0x7f {
			buf.WriteByte(b)
		}
	}
	return buf.String()
}

// EmitTerminalNotification writes sanitized OSC 777 and OSC 9 escape sequences to out.
func EmitTerminalNotification(out io.Writer, title, msg string) {
	if out == nil {
		return
	}
	cleanTitle := SanitizeTerminalString(title)
	cleanMsg := SanitizeTerminalString(msg)
	// OSC 777 is standard for desktop notifications in modern terminals
	_, _ = fmt.Fprintf(out, "\x1b]777;notify;%s;%s\x1b\\", cleanTitle, cleanMsg)
	// OSC 9 is supported by iTerm2
	_, _ = fmt.Fprintf(out, "\x1b]9;%s\x1b\\", cleanMsg)
}

// Dispatch sends the notification using the configured delivery channels.
func Dispatch(n Notification, cfg NotifyConfig, out io.Writer) error {
	if !cfg.Enabled {
		return nil
	}
	if n.State == StateBlocked && !cfg.OnBlocked {
		return nil
	}
	if n.State == StateIdle && !cfg.OnIdle {
		return nil
	}

	title := n.FormattedTitle()
	msg := n.FormattedMessage()

	// Terminal bell
	if cfg.Bell && out != nil {
		_, _ = fmt.Fprint(out, "\a")
	}

	// OSC 9 / OSC 777
	if cfg.OSC && out != nil {
		EmitTerminalNotification(out, title, msg)
	}

	// Custom command
	if cfg.Command != "" {
		cmd := BuildCustomNotifyCommand(cfg.Command, n, title, msg)
		return runNotification(cmd)
	}

	// Desktop notification
	if cfg.Desktop {
		SendDesktopNotification(title, msg)
	}

	return nil
}

// WindowsToastScript is the PowerShell script used to display Windows toast notifications
// without string interpolation vulnerabilities.
const WindowsToastScript = `
$title = $env:WYRM_NOTIFY_TITLE
$msg = $env:WYRM_NOTIFY_MSG
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$textNodes = $template.GetElementsByTagName('text')
$textNodes.Item(0).AppendChild($template.CreateTextNode($title)) > $null
$textNodes.Item(1).AppendChild($template.CreateTextNode($msg)) > $null
$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Wyrm').Show($toast)
`

// BuildWindowsToastCommand constructs an exec.Cmd for PowerShell Windows toast notifications
// safely passing title and message through environment variables to prevent command injection.
func BuildWindowsToastCommand(title, msg string) *exec.Cmd {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", WindowsToastScript)
	cmd.Env = append(os.Environ(),
		"WYRM_NOTIFY_TITLE="+title,
		"WYRM_NOTIFY_MSG="+msg,
	)
	return cmd
}

// SendDesktopNotification invokes platform desktop notification tools.
// Swappable for testing.
var SendDesktopNotification = func(title, msg string) {
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, msg, title)
		_ = runNotification(exec.Command("osascript", "-e", script))
	case "linux", "freebsd", "openbsd", "netbsd":
		_ = runNotification(exec.Command("notify-send", title, msg))
	case "windows":
		cmd := BuildWindowsToastCommand(title, msg)
		_ = runNotification(cmd)
	}
}

func runNotification(cmd *exec.Cmd) error {
	bounded, cancel := process.Command(cmd.Path, cmd.Args[1:]...)
	defer cancel()
	bounded.Env = cmd.Env
	return bounded.Run()
}
