package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// On Linux the sandbox is bubblewrap: the command sees the file system read
// only, but for what it may write and a /dev of its own, and has a network
// namespace of its own, in which only the loopback interface is up. A
// network namespace does not reach the sockets of the file system, and one
// like the socket of Docker is a way out: a filter of seccomp refuses
// sockets of the family AF_UNIX, and io_uring, which could open them.
//
// Landlock alone is no sandbox for a build: it limits TCP by port, not by
// address, so the loopback interface cannot be told from the network, and
// it does not limit connecting to a socket of the file system. Unprivileged
// user namespaces of a program's own are refused on Ubuntu 24.04 and later,
// while bubblewrap has a profile of AppArmor there that allows them.

// bwraps are where bubblewrap is looked for: not in PATH, where a tool for
// files could put a program of the same name. Tests replace them.
var bwraps = []string{"/usr/bin/bwrap", "/bin/bwrap"}

// find returns the bubblewrap that is installed, "" if none is.
func find() string {
	for _, b := range bwraps {
		if _, err := os.Stat(b); err == nil {
			return b
		}
	}
	return ""
}

// available runs a command that does nothing in the sandbox.
func available() error {
	b := find()
	if b == "" {
		return fmt.Errorf("%w: bubblewrap is not installed (the package bubblewrap, as %s)", ErrUnavailable, bwraps[0])
	}
	truth, err := exec.LookPath("true")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	cmd, err := wrap(context.Background(), b, nil, truth, nil)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: bubblewrap does not run here: %v: %s", ErrUnavailable, err, bytes.TrimSpace(out))
	}
	return nil
}

func command(ctx context.Context, writable []string, path string, args []string) (*exec.Cmd, error) {
	return wrap(ctx, find(), writable, path, args) // Available found it
}

// pipe makes the pipe the filter goes through; tests replace it.
var pipe = os.Pipe

// wrap returns the command path with args in the bubblewrap at b.
func wrap(ctx context.Context, b string, writable []string, path string, args []string) (*exec.Cmd, error) {
	prog, err := filter()
	if err != nil {
		return nil, err
	}
	// The program goes to bubblewrap as its file descriptor 3.
	r, w, err := pipe()
	if err != nil {
		return nil, err
	}
	go func() {
		w.Write(prog)
		w.Close()
	}()
	flags := []string{"--die-with-parent", "--unshare-net", "--ro-bind", "/", "/", "--dev", "/dev"}
	for _, p := range writable {
		flags = append(flags, "--bind", p, p)
	}
	flags = append(flags, "--seccomp", "3", "--", path)
	cmd := exec.CommandContext(ctx, b, append(flags, args...)...)
	cmd.ExtraFiles = []*os.File{r}
	return cmd, nil
}

// The parts of a program of seccomp.
const (
	ldAbs  = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
	jeq    = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
	jge    = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
	ret    = unix.BPF_RET | unix.BPF_K
	allowR = unix.SECCOMP_RET_ALLOW
	denyR  = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)

	// The offsets in struct seccomp_data of the number of the call, of the
	// architecture and of the low half of the first argument.
	offNr   = 0
	offArch = 4
	offArg0 = 16

	// x32 is the bit of the calls of the x32 ABI on amd64.
	x32 = 0x40000000
)

// arches are the architectures the filter is written for: the one the
// kernel names, and the numbers of socket and io_uring_setup there.
var arches = map[string]struct{ audit, socket, ioUring uint32 }{
	"amd64": {unix.AUDIT_ARCH_X86_64, 41, 425},
	"arm64": {unix.AUDIT_ARCH_AARCH64, 198, 425},
}

// arch is the architecture of this program; tests replace it.
var arch = runtime.GOARCH

// filter is the program of seccomp that refuses sockets of the family
// AF_UNIX, io_uring, and every call of another architecture or ABI, with
// EPERM, as struct sock_filter one after the other.
func filter() ([]byte, error) {
	a, ok := arches[arch]
	if !ok {
		return nil, fmt.Errorf("%w: no filter of seccomp for %s", ErrUnavailable, arch)
	}
	prog := []unix.SockFilter{
		{Code: ldAbs, K: offArch},
		{Code: jeq, K: a.audit, Jt: 1},
		{Code: ret, K: denyR},
		{Code: ldAbs, K: offNr},
		{Code: jge, K: x32, Jt: 4},
		{Code: jeq, K: a.ioUring, Jt: 3},
		{Code: jeq, K: a.socket, Jf: 3},
		{Code: ldAbs, K: offArg0},
		{Code: jeq, K: unix.AF_UNIX, Jf: 1},
		{Code: ret, K: denyR},
		{Code: ret, K: allowR},
	}
	var b bytes.Buffer
	binary.Write(&b, binary.NativeEndian, prog) // fixed sizes: cannot fail
	return b.Bytes(), nil
}
