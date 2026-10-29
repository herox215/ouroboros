//go:build !unix

package procgroup

import "os/exec"

// Detach leaves cmd as it is: here a process has no group of its own, and
// the end of the context kills only cmd itself.
func Detach(*exec.Cmd) {}

// Kill kills cmd, which was started; what it started lives on here.
func Kill(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
