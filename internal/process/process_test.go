package process

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestCommandCancellation(t *testing.T) {
	cmd, cancel := Command(os.Args[0])
	if cmd.WaitDelay <= 0 {
		t.Fatal("pipe reads must also have a deadline")
	}
	cancel()
	if err := cmd.Run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command: %v", err)
	}
}
