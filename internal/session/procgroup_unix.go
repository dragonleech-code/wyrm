//go:build unix

package session

import (
	"os/exec"
	"syscall"
)

// killGroupOnCancel starts cmd in its own process group and makes cancelling
// it kill the whole group, not just the shell.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
