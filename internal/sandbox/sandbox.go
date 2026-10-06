// Package sandbox runs a command without network and with writes only where
// it is allowed to: what the tool rebuild builds and tests runs there. Every
// process the command starts is in the same sandbox.
//
// "Without network" means without a way off the machine: the command may
// listen and connect on the loopback interface, as the tests of ouro start
// nodes there, and nothing else. Reading is not limited.
//
// Neither can it reach a socket of the file system: one like the socket of
// Docker would be a way out.
//
// On macOS the sandbox is sandbox-exec with a profile, on Linux bubblewrap
// with a filter of seccomp. Where neither can be had, Command fails with
// ErrUnavailable: see Available.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Policy is what a command in the sandbox may do besides reading and the
// loopback interface.
type Policy struct {
	// Writable are the files and directories it may write, each with
	// everything below it. Each has to exist, and be absolute.
	Writable []string
}

// Env is set to 1 for a command in the sandbox. Sandboxes do not nest: a
// command in one cannot start another one, see Nested.
const Env = "OURO_SANDBOX"

// Nested reports whether this process runs in the sandbox, where no other
// sandbox can be started.
func Nested() bool { return os.Getenv(Env) != "" }

// ErrUnavailable is the error of a system on which no sandbox can be had.
var ErrUnavailable = errors.New("no sandbox is available on this system")

// Available reports why there is no sandbox on this system, nil if there is
// one.
func Available() error {
	if Nested() {
		return fmt.Errorf("%w: this process runs in a sandbox already, and sandboxes do not nest", ErrUnavailable)
	}
	return available()
}

// Command returns the command name with args, to run in the sandbox of p.
// name is looked up in PATH as exec.Command does. Its environment is that of
// this process with Env: add to it rather than replace it.
func Command(ctx context.Context, p Policy, name string, args ...string) (*exec.Cmd, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	writable := make([]string, len(p.Writable))
	for i, w := range p.Writable {
		if !filepath.IsAbs(w) {
			return nil, fmt.Errorf("the writable path %q is not absolute", w)
		}
		// The sandbox sees a path where it leads.
		real, err := filepath.EvalSymlinks(w)
		if err != nil {
			return nil, err
		}
		writable[i] = real
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd, err := command(ctx, writable, path, args)
	if err == nil {
		cmd.Env = append(os.Environ(), Env+"=1")
	}
	return cmd, err
}
