//go:build !unix

package runner

import "os/exec"

// ownProcessGroup is a no-op where process groups are not available.
func ownProcessGroup(*exec.Cmd) {}

// killProcessGroup kills only the program where process groups are not available.
func killProcessGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
