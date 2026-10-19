//go:build !unix

package claudecode

import "os/exec"

// group does nothing where a process has no group of its own.
func group(*exec.Cmd) {}

// kill ends cmd, which was started.
func kill(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
