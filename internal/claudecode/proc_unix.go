//go:build unix

package claudecode

import (
	"os/exec"
	"syscall"
)

// group makes cmd the first of a group of its own, so that what it starts is
// ended with it: the CLI may be a script that runs node.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return kill(cmd) }
}

// kill ends the group of cmd, which was started.
func kill(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
