// Package sandbox runs a command with writes only where it is allowed to, and
// without network unless it is allowed that: what the tool bash runs. Every
// process the command starts is in the same sandbox.
//
// "Without network" means without a way off the machine: the command may
// listen and connect on the loopback interface, as the tests of ouro start
// nodes there, and nothing else. Reading is limited only by Hidden.
//
// Neither can it reach a socket of the file system, with network or without:
// one like the socket of Docker would be a way out.
//
// On macOS the sandbox is sandbox-exec with a profile, on Linux bubblewrap
// with a filter of seccomp. Where neither can be had, Command fails with
// ErrUnavailable: see Available.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// Policy is what a command in the sandbox may do besides reading and the
// loopback interface. Every path in it has to be absolute.
type Policy struct {
	// Writable are the files and directories it may write, each with
	// everything below it. Each has to exist.
	Writable []string
	// ReadOnly are files and directories within Writable it may not write,
	// each with everything below it. One that does not exist may not be
	// created either, and a symbolic link counts as itself and as where it
	// leads.
	ReadOnly []string
	// Hidden are files and directories it may neither read nor write, each
	// with everything below it, counted like ReadOnly.
	Hidden []string
	// Network lets it reach other hosts. Sockets of the file system stay
	// out of reach.
	Network bool
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
	// The sandbox sees a path where it leads.
	resolved := Policy{ReadOnly: reals(p.ReadOnly), Hidden: reals(p.Hidden), Network: p.Network}
	for _, w := range p.Writable {
		if !filepath.IsAbs(w) {
			return nil, fmt.Errorf("the writable path %q is not absolute", w)
		}
		real, err := filepath.EvalSymlinks(w)
		if err != nil {
			return nil, err
		}
		resolved.Writable = append(resolved.Writable, real)
	}
	for _, path := range slices.Concat(p.ReadOnly, p.Hidden) {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("the path %q of the sandbox is not absolute", path)
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	cmd, err := command(ctx, resolved, path, args)
	if err == nil {
		cmd.Env = append(os.Environ(), Env+"=1")
	}
	return cmd, err
}

// reals returns where paths lead, for rules that close them: each as
// itself, below where the directory above it leads, and a symbolic link also
// as where it leads.
func reals(paths []string) []string {
	var out []string
	for _, p := range paths {
		self := filepath.Join(where(filepath.Dir(p)), filepath.Base(p))
		out = append(out, self)
		if info, err := os.Lstat(self); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			out = append(out, where(p))
		}
	}
	return out
}

// where is where the path p leads. What does not exist is taken as it is
// written, below the nearest directory above it that leads somewhere, and
// so is a symbolic link in it that leads nowhere yet.
func where(p string) string {
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = filepath.Dir(p)
	}
}
