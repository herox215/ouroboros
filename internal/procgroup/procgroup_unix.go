//go:build unix

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Detach makes cmd, before it starts, the first process of a group of its
// own, which is killed when the context of cmd ends.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return Kill(cmd) }
}

// Kill kills the group of cmd, which was started with Detach. A group that
// is gone already is os.ErrProcessDone.
func Kill(cmd *exec.Cmd) error {
	pgid, err := group(cmd)
	if err != nil {
		return err
	}
	err = syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// group is the ID of the group of cmd: the PID of its first process. A PID
// of 1 or less is refused, as kill(2) would take its negative for the group
// of the caller or for every process it may signal.
func group(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, errors.New("procgroup: the command was not started")
	}
	if cmd.Process.Pid <= 1 {
		return 0, fmt.Errorf("procgroup: refusing to signal the group of PID %d", cmd.Process.Pid)
	}
	return cmd.Process.Pid, nil
}
