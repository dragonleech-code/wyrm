//go:build !unix

package session

import "os/exec"

// killGroupOnCancel leaves exec's default cancellation (kill the process).
func killGroupOnCancel(*exec.Cmd) {}
