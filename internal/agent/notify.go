package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

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

// SanitizeTerminalString strips C0 control characters, DEL, and the C1
// controls (U+0080–U+009F) to prevent terminal escape sequence injection. C1
// matters because some terminals treat U+009B as CSI and U+009D as OSC, the
// same as ESC [ and ESC ]. Invalid UTF-8 is dropped rather than passed
// through, since a terminal may decode it differently than Go does.
func SanitizeTerminalString(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == utf8.RuneError {
			continue
		}
		buf.WriteRune(r)
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

// macNotifyScript reads the message and title from osascript's own argument
// list. Building them into the script with Go's %q quoting was not AppleScript
// quoting: Go escapes such as \u001b mean nothing to AppleScript, so a crafted
// window name could break the notification. As arguments they are data.
const macNotifyScript = `on run argv
display notification (item 1 of argv) with title (item 2 of argv)
end run`

// BuildMacNotifyCommand constructs the osascript invocation for a macOS
// notification, passing title and message as arguments rather than code.
//
// osascript stops reading options at its first non-option argument, which is
// the message — so only a message that itself starts with "-" could be taken
// for an option. A leading space defuses that without depending on how
// osascript treats "--".
func BuildMacNotifyCommand(title, msg string) *exec.Cmd {
	if strings.HasPrefix(msg, "-") {
		msg = " " + msg
	}
	return exec.Command("osascript", "-e", macNotifyScript, msg, title)
}

// SendDesktopNotification invokes platform desktop notification tools.
// Swappable for testing.
var SendDesktopNotification = func(title, msg string) {
	switch runtime.GOOS {
	case "darwin":
		_ = runNotification(BuildMacNotifyCommand(title, msg))
	case "linux", "freebsd", "openbsd", "netbsd":
		// "--" so a title starting with "-" is never read as an option.
		_ = runNotification(exec.Command("notify-send", "--", title, msg))
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
