package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// On Linux the sandbox is bubblewrap: the command sees the file system read
// only, but for what it may write and a /dev of its own, and without network
// has a network namespace of its own, in which only the loopback interface is
// up. What is hidden is covered: a file by an empty one, a directory by an
// empty one in memory. A
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
	cmd, err := wrap(context.Background(), b, Policy{}, truth, nil)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: bubblewrap does not run here: %v: %s", ErrUnavailable, err, bytes.TrimSpace(out))
	}
	return nil
}

// stop finds nothing: the namespace of processes of the command ended all
// it started, see wrap.
func stop(int) bool { return false }

func command(ctx context.Context, p Policy, path string, args []string) (*exec.Cmd, error) {
	return wrap(ctx, find(), p, path, args) // Available found it
}

// pipe makes the pipe the filter goes through; tests replace it.
var pipe = os.Pipe

// wrap returns the command path with args in the bubblewrap at b, kept to
// what p allows.
func wrap(ctx context.Context, b string, p Policy, path string, args []string) (*exec.Cmd, error) {
	prog, err := filter()
	if err != nil {
		return nil, err
	}
	mounted, err := mounts(p)
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
	// The command has a namespace of processes of its own: once it ends,
	// what it started ends with it, also what left its group.
	flags := []string{"--die-with-parent", "--unshare-pid"}
	if !p.Network {
		flags = append(flags, "--unshare-net")
	}
	flags = append(flags, "--ro-bind", "/", "/")
	flags = append(flags, mounted...)
	// /dev comes last: a writable / would cover it.
	flags = append(flags, "--dev", "/dev", "--seccomp", "3", "--", path)
	cmd := exec.CommandContext(ctx, b, append(flags, args...)...)
	cmd.ExtraFiles = []*os.File{r}
	return cmd, nil
}

// mount is what bubblewrap mounts at one path, as its flags.
type mount struct {
	at    string
	flags []string
}

// mounts are the flags of bubblewrap that mount what p allows over a root
// that is read only. A mount hides what is mounted below it before, so they
// go from the root down, and what is read only or hidden is left out of
// every mount that makes it writable.
//
// A path that is read only and does not exist cannot be mounted over, nor can
// a symbolic link, as the mount would go where it leads: the directory above
// it that exists is mounted read only instead, and what is in it but links
// writable again, so that nothing new is created there and no link changed.
// What a link leads to, a policy names besides, see reals. A directory this
// user cannot change needs none of that, and one like /usr/bin has thousands
// of entries, each a mount that bubblewrap takes its time for.
func mounts(p Policy) ([]string, error) {
	closed := slices.Concat(p.ReadOnly, p.Hidden)
	var all []mount
	writable := func(at string) {
		if !slices.ContainsFunc(closed, func(c string) bool { return within(at, c) }) {
			all = append(all, mount{at, []string{"--bind", at, at}})
		}
	}
	for _, w := range p.Writable {
		writable(w)
	}
	for _, r := range p.ReadOnly {
		info, err := os.Lstat(r)
		if err == nil && info.Mode()&fs.ModeSymlink == 0 {
			all = append(all, mount{r, []string{"--ro-bind", r, r}})
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if !slices.ContainsFunc(p.Writable, func(w string) bool { return within(r, w) }) {
			continue // nothing is created where nothing is written
		}
		// The directory the link is in, or the nearest above what is
		// missing that exists.
		dir := filepath.Dir(r)
		for err != nil {
			if _, err = os.Lstat(dir); err != nil {
				dir = filepath.Dir(dir)
			}
		}
		if !changeable(dir) {
			continue // nothing is created there and no link changed as it is
		}
		all = append(all, mount{dir, []string{"--ro-bind", dir, dir}})
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			// A link is not written through: what it leads to is mounted
			// for where it is.
			if e.Type()&fs.ModeSymlink == 0 {
				writable(filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, h := range p.Hidden {
		info, err := os.Lstat(h)
		if errors.Is(err, fs.ErrNotExist) || err == nil && info.Mode()&fs.ModeSymlink != 0 {
			continue // there is nothing to read, or nothing but where it leads
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			all = append(all, mount{h, []string{"--tmpfs", h, "--remount-ro", h}})
		} else {
			all = append(all, mount{h, []string{"--ro-bind", os.DevNull, h}})
		}
	}
	slices.SortStableFunc(all, func(a, b mount) int { return depth(a.at) - depth(b.at) })
	var flags []string
	seen := map[string]bool{}
	for _, m := range all {
		// A directory closed for two paths is mounted once.
		if key := strings.Join(m.flags, "\x00"); !seen[key] {
			seen[key] = true
			flags = append(flags, m.flags...)
		}
	}
	return flags, nil
}

// changeable reports whether this user can change what is in dir: by
// writing there, or as its owner, who can make it writable. Tests replace it.
var changeable = func(dir string) bool {
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		return true
	}
	return int(st.Uid) == os.Geteuid() || unix.Faccessat(unix.AT_FDCWD, dir, unix.W_OK, unix.AT_EACCESS) == nil
}

// depth is how many directories path is below the root.
func depth(path string) int {
	if path == "/" {
		return 0
	}
	return strings.Count(path, "/")
}

// within reports whether path is root or below it.
func within(path, root string) bool {
	return path == root || root == "/" || strings.HasPrefix(path, root+"/")
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
