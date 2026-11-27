//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd as the leader of a new process group, so a stop reaches everything
// it spawns.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills cmd's whole process group: the program and every descendant still in it.
func killProcessGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
