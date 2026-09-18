// Package clipboard writes text to the system clipboard and formats OSC 52 sequences.
package clipboard

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/jskoll/wyrm/internal/process"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrNoBackend means no clipboard tool was found for this platform. It is a
// real, reportable outcome rather than a silent success: on a headless Linux
// box, in a container, or over SSH without X, there is frequently no backend
// at all, and returning nil made every caller announce a copy that never
// happened.
var ErrNoBackend = errors.New("no clipboard tool available")

// OSC52 formats text as an OSC 52 clipboard escape sequence.
//
// This is the only mechanism that reaches the *local* clipboard from a remote
// or headless host, but it has to be written to the terminal — which the TUI
// cannot do while Bubble Tea owns the screen (v1's renderer writes from its
// own goroutine, drops tea.Printf under the alt screen, and has no clipboard
// command). So it is currently used by callers that own their own output, and
// deliberately not wired into internal/tui. See WriteWithOSC.
func OSC52(text string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if os.Getenv("TMUX") != "" {
		// In tmux, wrap in DCS passthrough
		return fmt.Sprintf("\x1bPtmux;\x1b\x1b]52;c;%s\x07\x1b\\", encoded)
	}
	return fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
}

// Write copies text to the system clipboard, returning ErrNoBackend when the
// platform has no tool to do it with.
func Write(text string) error {
	return writeSystemClipboard(text)
}

// Backends names the tools Write looks for on this platform, for an error
// message that tells the user what to install rather than just that it failed.
func Backends() string {
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy"
	case "windows":
		return "powershell"
	default:
		return strings.Join([]string{"wl-copy", "xclip", "xsel"}, ", ")
	}
}

// WriteWithOSC copies to system clipboard and also writes OSC 52 to w if non-nil.
func WriteWithOSC(text string, w io.Writer) error {
	if w != nil {
		_, _ = fmt.Fprint(w, OSC52(text))
	}
	return Write(text)
}

// backend returns the command that copies stdin to this platform's clipboard,
// or ErrNoBackend when none is installed.
//
// Split out from writeSystemClipboard so Available can answer "could this
// work?" without running it: probing by copying an empty string would clobber
// whatever the user last put on their clipboard.
func backend() (name string, args []string, err error) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("pbcopy"); err == nil {
			return "pbcopy", nil, nil
		}
	case "linux", "freebsd", "openbsd", "netbsd":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if _, err := exec.LookPath("wl-copy"); err == nil {
				return "wl-copy", nil, nil
			}
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			return "xclip", []string{"-selection", "clipboard"}, nil
		}
		if _, err := exec.LookPath("xsel"); err == nil {
			return "xsel", []string{"--clipboard", "--input"}, nil
		}
	case "windows":
		if _, err := exec.LookPath("powershell"); err == nil {
			return "powershell", []string{"-NoProfile", "-Command", "Set-Clipboard -Value $input"}, nil
		}
	}
	return "", nil, ErrNoBackend
}

// Available reports whether a clipboard backend exists, without touching the
// clipboard itself.
func Available() error {
	_, _, err := backend()
	return err
}

var writeSystemClipboard = func(text string) error {
	name, args, err := backend()
	if err != nil {
		return err
	}
	cmd, cancel := process.Command(name, args...)
	defer cancel()
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
