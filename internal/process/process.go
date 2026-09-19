// Package process bounds non-interactive helper processes. Interactive programs
// and user lifecycle hooks intentionally retain their own lifetime.
package process

import (
	"context"
	"os/exec"
	"time"
)

const timeout = 10 * time.Second

// Command returns a command and a cancellation function the caller must defer.
func Command(name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	return cmd, cancel
}
