//go:build unix

package runner

import (
	"errors"
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd as the leader of a new process group, so a stop reaches everything
// it spawns.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills cmd's whole process group: the program and every descendant still in it.
// A group with nothing left in it is fine.
func killProcessGroup(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
