//go:build !unix

package tui

import "os/exec"

// detached leaves cmd as it is: here a timeout kills only the renderer
// itself, not the children it may have started.
func detached(*exec.Cmd) {}
