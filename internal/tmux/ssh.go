package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jskoll/wyrm/internal/process"
)

// SSH is a Runner that drives tmux on another machine over ssh: every command
// becomes `ssh HOST -- 'tmux' 'arg' ...`, quoted for the remote shell.
//
// Connections are multiplexed through one ssh ControlMaster, so a build's
// dozens of tmux calls cost one handshake rather than one each. Its socket
// lives in a directory only this user can enter (see controlDir); a
// predictable path in /tmp could be pre-created by another user.
type SSH struct {
	Host string
	// SocketName and Command mean what they do on Exec, on the remote side.
	SocketName string
	Command    string
}

// Quote wraps s in single quotes for a POSIX shell or fish. Both read '\”
// as "end the quote, a literal quote, start again", so the result means the
// same thing whichever the remote login shell is.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// QuoteArgs quotes each argument and joins them into one command line.
func QuoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = Quote(a)
	}
	return strings.Join(quoted, " ")
}

func (s SSH) tmuxArgs(args []string) []string {
	bin := s.Command
	if bin == "" {
		bin = "tmux"
	}
	out := []string{bin}
	if s.SocketName != "" {
		out = append(out, "-L", s.SocketName)
	}
	return append(out, args...)
}

// sshArgs is the ssh invocation for a remote command line. tty asks for a
// terminal and allows ssh to prompt (for a passphrase or host key), which
// only attaching needs; everything else runs in batch mode, so a missing key
// fails at once instead of hanging on a prompt nobody can see.
func (s SSH) sshArgs(remote string, tty bool) []string {
	args := []string{"-T", "-o", "BatchMode=yes"}
	if tty {
		args = []string{"-t"}
	}
	if dir, err := controlDir(); err == nil {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+filepath.Join(dir, "%C"),
			"-o", "ControlPersist=60")
	}
	return append(args, s.Host, "--", remote)
}

// Run implements Runner. As with Exec, stdout alone is returned on success
// and stderr in its place on failure.
func (s SSH) Run(args ...string) (string, error) {
	cmd, cancel := process.Command("ssh", s.sshArgs(QuoteArgs(s.tmuxArgs(args)), false)...)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if noServerText(msg) {
				return msg, fmt.Errorf("%w: %w", ErrNoServer, err)
			}
			return msg, err
		}
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

// Attach attaches the terminal to a remote session.
func (s SSH) Attach(target string) error {
	cmd := exec.Command("ssh", s.sshArgs(QuoteArgs(s.tmuxArgs([]string{"attach-session", "-t", target})), true)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Home returns the remote user's home directory.
func (s SSH) Home() (string, error) {
	cmd, cancel := process.Command("ssh", s.sshArgs(`printf %s "$HOME"`, false)...)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("asking %s for its home directory: %w", s.Host, err)
	}
	home := strings.TrimSpace(string(out))
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("%s reported home directory %q", s.Host, home)
	}
	return home, nil
}

// RunShell runs command through sh on the remote host, in dir, with env
// added, streaming its output to w. A timeout ends the local ssh, which ends
// the remote command with it.
func (s SSH) RunShell(command, dir string, env []string, timeout time.Duration, w io.Writer) error {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "ssh", s.sshArgs(ShellCommand(command, dir, env), false)...)
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

// ShellCommand is the remote command line RunShell runs.
func ShellCommand(command, dir string, env []string) string {
	remote := "cd " + Quote(dir) + " && "
	if len(env) > 0 {
		remote += "env " + QuoteArgs(env) + " "
	}
	return remote + "sh -c " + Quote(command)
}

// controlDir is a 0700 directory for ssh control sockets.
func controlDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "wyrm", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs its execute bit; 0700 is owner-only
		return "", err
	}
	return dir, nil
}
