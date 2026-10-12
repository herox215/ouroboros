//go:build !linux && !darwin

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func available() error { return fmt.Errorf("%w on %s", ErrUnavailable, runtime.GOOS) }

func stop(int) bool { return false } // no sandbox started a command

func command(context.Context, Policy, string, []string) (*exec.Cmd, error) {
	return nil, available()
}
