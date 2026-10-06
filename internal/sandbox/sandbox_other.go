//go:build !linux && !darwin

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func available() error { return fmt.Errorf("%w on %s", ErrUnavailable, runtime.GOOS) }

func command(context.Context, []string, string, []string) (*exec.Cmd, error) {
	return nil, available()
}
