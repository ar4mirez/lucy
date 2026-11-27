//go:build !unix

package runner

import (
	"errors"
	"os"
	"os/exec"
)

// ownProcessGroup is a no-op where process groups are not available.
func ownProcessGroup(*exec.Cmd) {}

// killProcessGroup kills only the program where process groups are not available. A program that
// has already exited is fine.
func killProcessGroup(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
