// Package tmux wraps execution of tmux commands.
package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jskoll/wyrm/internal/process"
)

// Runner executes a tmux command and returns its combined output, trimmed.
// Tests substitute a recording mock to assert command sequences without a
// tmux server.
type Runner interface {
	Run(args ...string) (string, error)
}

// Exec is the real Runner, shelling out to the tmux binary on PATH.
type Exec struct {
	// SocketName selects a separate tmux server (tmux -L). Empty uses the
	// default server. Used by integration tests to stay isolated, and
	// configurable via [tmux].socket / WYRM_TMUX_SOCKET for real use.
	SocketName string
	// ConfigFile overrides the server startup config (-f). Integration tests
	// use os.DevNull so personal plugins and options cannot affect fixtures.
	ConfigFile string

	// Command overrides the binary invoked in place of "tmux" — a full path,
	// or a wrapper/fork name like "byobu" or "psmux". Empty uses "tmux",
	// resolved from PATH. Configurable via [tmux].command / WYRM_TMUX_COMMAND.
	Command string
}

// bin returns the binary name or path to invoke: Command if set, else "tmux".
func (e Exec) bin() string {
	if e.Command != "" {
		return e.Command
	}
	return "tmux"
}

// Run implements Runner. It captures stdout only, deliberately: the first
// command to start the tmux server (new-session) makes it source the user's
// ~/.tmux.conf, and any parse error in that file goes to stderr. Folding
// stderr into stdout — as CombinedOutput does — prepends that diagnostic to
// the output of exactly the commands whose "-F" format string is parsed
// positionally, silently poisoning the session/window/pane ID wyrm then
// targets everything else by.
//
// On failure the stderr text is returned in place of stdout, because that's
// where tmux puts its diagnostics and callers match on them (see
// FindSessionID's "no server running" handling).
func (e Exec) Run(args ...string) (string, error) {
	if e.ConfigFile != "" {
		args = append([]string{"-f", e.ConfigFile}, args...)
	}
	if e.SocketName != "" {
		args = append([]string{"-L", e.SocketName}, args...)
	}
	cmd, cancel := process.Command(e.bin(), args...)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			// Wrap the sentinel here, at the one place that can see tmux's
			// diagnostic, so callers can errors.Is it rather than each
			// re-matching the English text — see NoServerRunning.
			if noServerText(msg) {
				return msg, fmt.Errorf("%w: %w", ErrNoServer, err)
			}
			return msg, err
		}
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

// Sigils prefixing tmux's object IDs.
const (
	SessionSigil = '$'
	WindowSigil  = '@'
	PaneSigil    = '%'
)

// ValidID reports whether s is a well-formed tmux object ID for sigil: the
// sigil followed by at least one digit and nothing else.
func ValidID(sigil byte, s string) bool {
	if len(s) < 2 || s[0] != sigil {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ErrNoServer marks a tmux invocation that failed only because no server is up.
// Exec.Run wraps it around such failures so callers can test for the condition
// with errors.Is instead of re-matching tmux's English diagnostic.
var ErrNoServer = errors.New("no tmux server running")

// NoServerRunning reports whether a failed tmux invocation failed only
// because no server is up — which tmux signals by failing rather than by
// printing an empty list. That is an ordinary outcome ("nothing is running"),
// not a fault, and every caller that lists things has to tell the two apart.
//
// The typed check comes first. The text check remains as a fallback because a
// Runner other than Exec — the dry-run recorder, a test mock, a future one —
// may report the condition only in its output, and because the wording is not
// something tmux promises: "no server running on <socket>" for the default
// server, "error connecting to <socket> (No such file or directory)" for an -L
// socket that was never created, "server exited unexpectedly" for a client
// that reached the socket just as the server tore itself down — which tmux
// does automatically once its last session ends, so killing that last
// session and then listing sessions is a legitimate race, not a fault.
func NoServerRunning(err error, out string) bool {
	if errors.Is(err, ErrNoServer) {
		return true
	}
	return noServerText(out)
}

func noServerText(out string) bool {
	msg := strings.ToLower(out)
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "error connecting") ||
		strings.Contains(msg, "server exited unexpectedly")
}

// CheckID validates an ID parsed out of tmux's output. wyrm targets every
// command by ID rather than by name, so a malformed one doesn't fail loudly —
// it silently misdirects every subsequent command. Catching it at the parse
// site turns that into one clear error.
func CheckID(sigil byte, kind, s string) error {
	if ValidID(sigil, s) {
		return nil
	}
	return fmt.Errorf("expected a tmux %s id (like %c1), got %q", kind, sigil, s)
}

// InsideTmux reports whether the current process runs inside a tmux client.
func InsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// Attach hands the caller's terminal to a tmux client attached to target,
// which should be a tmux session ID (e.g. "$3") rather than a session name —
// see FindSessionID for why. It is a method on Exec, not part of Runner,
// because it needs the process's stdio rather than captured output — but it
// still has to honor the same SocketName/Command as every other call, or a
// session built on a named socket/binary would attach to the wrong server.
func (e Exec) Attach(target string) error {
	args := []string{"attach-session", "-t", target}
	if e.ConfigFile != "" {
		args = append([]string{"-f", e.ConfigFile}, args...)
	}
	if e.SocketName != "" {
		args = append([]string{"-L", e.SocketName}, args...)
	}
	cmd := exec.Command(e.bin(), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CurrentSession returns the ID and name of the tmux session wyrm is
// running inside (i.e. $TMUX is set). It's meaningless to call this outside
// tmux — callers should check InsideTmux first.
func CurrentSession(r Runner) (id, name string, err error) {
	out, err := r.Run("display-message", "-p", "-F", "#{session_id}|#{session_name}")
	if err != nil {
		return "", "", fmt.Errorf("finding current session: %w", CmdErr(err, out))
	}
	id, name, ok := strings.Cut(out, "|")
	if !ok {
		return "", "", fmt.Errorf("unexpected tmux output %q", out)
	}
	if err := CheckID(SessionSigil, "session", id); err != nil {
		return "", "", fmt.Errorf("finding current session: %w", err)
	}
	return id, name, nil
}

// SessionName returns the current name of the session identified by id,
// targeted by ID rather than name for the same reason FindSessionID is —
// see its doc. Used right after creating a session: tmux does not always
// name a session what was asked for (see the rename warning in
// session.Create), and the actual name can't safely share a delimited
// multi-field response with the IDs new-session also reports, because tmux
// accepts "|" in a session name and there is no way to tell where such a
// name ends and the next field begins.
func SessionName(r Runner, id string) (string, error) {
	out, err := r.Run("display-message", "-p", "-t", id, "-F", "#{session_name}")
	if err != nil {
		return "", fmt.Errorf("querying session name: %w", CmdErr(err, out))
	}
	return out, nil
}

// FindSessionID returns the tmux session ID for the exact session name, and
// whether a matching session exists. It lists every session and compares
// names in Go rather than passing the name through tmux's -t target syntax:
// that syntax treats "." as the window.pane separator, so a session name
// containing "." (e.g. "wyrm.vim") is misparsed by commands like
// has-session, kill-session, new-window, or attach-session — even with an
// "=" exact-match prefix, which only guards against prefix ambiguity, not
// this. Once found, the returned ID is a safe, unambiguous target for any
// command regardless of what characters the session's name contains.
func FindSessionID(r Runner, name string) (id string, ok bool, err error) {
	out, err := r.Run("list-sessions", "-F", "#{session_id}|#{session_name}")
	if err != nil {
		if NoServerRunning(err, out) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("listing sessions: %w", CmdErr(err, out))
	}
	type entry struct{ id, name string }
	var sessions []entry
	for _, line := range strings.Split(out, "\n") {
		sessID, sessName, found := strings.Cut(strings.TrimRight(line, "\r"), "|")
		if !found {
			continue
		}
		sessions = append(sessions, entry{sessID, sessName})
	}
	// Exact match first, then the sanitized form. Some tmux builds rewrite "."
	// and ":" to "_" when a session is created, so a project in a directory
	// called "example.com" ends up as the session "example_com". Matching only
	// exactly meant the second `wyrm` run never found it, tried to create a
	// duplicate, and failed — and `wyrm kill` could never find it at all.
	for _, want := range []string{name, SanitizeName(name)} {
		for _, s := range sessions {
			if s.name != want {
				continue
			}
			if err := CheckID(SessionSigil, "session", s.id); err != nil {
				return "", false, fmt.Errorf("listing sessions: %w", err)
			}
			return s.id, true, nil
		}
	}
	return "", false, nil
}

// nameSanitizer mirrors the substitution those tmux builds apply.
var nameSanitizer = strings.NewReplacer(".", "_", ":", "_")

// SanitizeName returns the session name tmux would use for name on the builds
// that rewrite "." and ":" — the characters its own -t target syntax reserves
// as the session:window.pane separators.
func SanitizeName(name string) string { return nameSanitizer.Replace(name) }
