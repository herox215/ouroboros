//go:build unix

package tui

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// detached puts cmd into a process group of its own and kills that group when
// cmd's context ends, so that no child of a renderer outlives its timeout.
func detached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
}
