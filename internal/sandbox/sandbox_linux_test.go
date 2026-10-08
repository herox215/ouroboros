package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// replace sets the hook to with for the rest of the test.
func replace[T any](t *testing.T, hook *T, with T) {
	t.Helper()
	old := *hook
	*hook = with
	t.Cleanup(func() { *hook = old })
}

// Without bubblewrap, or where it does not run, there is no sandbox.
func TestWithoutBubblewrap(t *testing.T) {
	t.Setenv(Env, "") // what is tested here starts no sandbox
	replace(t, &bwraps, []string{filepath.Join(t.TempDir(), "bwrap")})
	if err := Available(); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "bubblewrap is not installed") {
		t.Errorf("not installed: %v", err)
	}
	if _, err := Command(context.Background(), Policy{}, "true"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("command: %v", err)
	}

	broken := filepath.Join(t.TempDir(), "bwrap")
	os.WriteFile(broken, []byte("#!/bin/sh\necho no user namespaces >&2\nexit 1\n"), 0o755)
	replace(t, &bwraps, []string{filepath.Join(t.TempDir(), "missing"), broken})
	if err := Available(); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "bubblewrap does not run here: exit status 1: no user namespaces") {
		t.Errorf("does not run: %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	if err := Available(); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), `"true"`) {
		t.Errorf("without true: %v", err)
	}
}

// The filter is written for the architectures it knows; on another one
// there is no sandbox. Without a pipe for it there is no command.
func TestFilterFails(t *testing.T) {
	t.Setenv(Env, "")
	replace(t, &arch, "mips")
	if err := Available(); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "no filter of seccomp for mips") {
		t.Errorf("another architecture: %v", err)
	}
	replace(t, &arch, "amd64")
	broken := errors.New("no pipe")
	replace(t, &pipe, func() (*os.File, *os.File, error) { return nil, nil, broken })
	if _, err := wrap(context.Background(), "/usr/bin/bwrap", Policy{}, "true", nil); !errors.Is(err, broken) {
		t.Errorf("without a pipe: %v", err)
	}
}

// The filter refuses sockets of the file system, io_uring, and calls of
// another architecture or ABI, and lets everything else through.
func TestFilter(t *testing.T) {
	for _, a := range []string{"amd64", "arm64"} {
		replace(t, &arch, a)
		b, err := filter()
		if err != nil {
			t.Fatal(err)
		}
		prog := make([]unix.SockFilter, len(b)/8)
		binary.Read(bytes.NewReader(b), binary.NativeEndian, prog)
		at := arches[a]
		for _, tt := range []struct {
			name            string
			audit, nr, arg0 uint32
			want            uint32
		}{
			{"a socket of the file system", at.audit, at.socket, unix.AF_UNIX, denyR},
			{"a socket of the network", at.audit, at.socket, unix.AF_INET, allowR},
			{"io_uring", at.audit, at.ioUring, 0, denyR},
			{"another call", at.audit, 1, unix.AF_UNIX, allowR},
			{"another architecture", 0x40000003, at.socket, unix.AF_INET, denyR},
			{"the x32 ABI", at.audit, x32 | 41, unix.AF_UNIX, denyR},
		} {
			if got := run(prog, tt.nr, tt.audit, tt.arg0); got != tt.want {
				t.Errorf("%s, %s: %#x, want %#x", a, tt.name, got, tt.want)
			}
		}
	}
}

// run interprets prog for a call, as the kernel would.
func run(prog []unix.SockFilter, nr, audit, arg0 uint32) uint32 {
	var acc uint32
	for pc := 0; pc < len(prog); pc++ {
		f := prog[pc]
		switch f.Code {
		case ldAbs:
			acc = map[uint32]uint32{offNr: nr, offArch: audit, offArg0: arg0}[f.K]
		case jeq, jge:
			if acc == f.K || f.Code == jge && acc > f.K {
				pc += int(f.Jt)
			} else {
				pc += int(f.Jf)
			}
		case ret:
			return f.K
		}
	}
	panic("the program does not end in ret")
}

// What is writable is mounted from the root down, without what is read only
// or hidden in it; what is read only and missing closes the directory above
// it to anything new; what is hidden is covered.
func TestMounts(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"w/sub/keep", "w/ro", "w/secrets", "w/closed/inner"} {
		os.MkdirAll(filepath.Join(dir, d), 0o700)
	}
	os.WriteFile(filepath.Join(dir, "w/sub/file"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "w/secret"), nil, 0o600)
	os.Symlink("/etc", filepath.Join(dir, "w/sub/link"))
	os.Symlink("/etc", filepath.Join(dir, "w/ro/link"))
	os.Symlink("/etc", filepath.Join(dir, "w/linked"))
	at := func(p string) string { return filepath.Join(dir, p) }
	got, err := mounts(Policy{
		Writable: []string{at("w"), at("w/ro/inner"), at("w/secrets/inner")},
		ReadOnly: []string{at("w/ro"), at("w/sub/missing"), at("w/closed"), at("w/closed/inner/missing"), at("elsewhere/missing"), at("w/ro/link")},
		Hidden:   []string{at("w/secret"), at("w/secrets"), at("w/none"), at("w/linked")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--bind", at("w"), at("w"),
		"--ro-bind", at("w/ro"), at("w/ro"),
		"--ro-bind", at("w/sub"), at("w/sub"),
		"--ro-bind", at("w/closed"), at("w/closed"),
		"--ro-bind", os.DevNull, at("w/secret"),
		"--tmpfs", at("w/secrets"), "--remount-ro", at("w/secrets"),
		"--bind", at("w/sub/file"), at("w/sub/file"),
		"--bind", at("w/sub/keep"), at("w/sub/keep"),
		"--ro-bind", at("w/closed/inner"), at("w/closed/inner"),
	}
	// The link in w/ro closes w/ro, which is closed already; the hidden
	// link leaves nothing to cover but where it leads.
	if !slices.Equal(got, want) {
		t.Errorf("mounts:\n%q\nwant\n%q", got, want)
	}

	if got, err := mounts(Policy{Writable: []string{"/"}}); err != nil || !slices.Equal(got, []string{"--bind", "/", "/"}) {
		t.Errorf("the root: %q, %v", got, err)
	}
	// A directory that cannot be looked into, or not listed, is no answer.
	locked, blind := at("locked"), at("blind")
	os.Mkdir(locked, 0)
	os.Mkdir(blind, 0o100)
	t.Cleanup(func() { os.Chmod(locked, 0o700); os.Chmod(blind, 0o700) })
	if os.Geteuid() != 0 {
		for _, p := range []Policy{
			{Writable: []string{dir}, ReadOnly: []string{filepath.Join(locked, "x")}},
			{Writable: []string{dir}, ReadOnly: []string{filepath.Join(blind, "x")}},
			{Hidden: []string{filepath.Join(locked, "x")}},
		} {
			if _, err := mounts(p); err == nil {
				t.Errorf("below a directory that cannot be read, %+v: no error", p)
			}
		}
		if _, err := wrap(context.Background(), "/usr/bin/bwrap", Policy{Hidden: []string{filepath.Join(locked, "x")}}, "true", nil); err == nil {
			t.Error("a policy that cannot be mounted should fail")
		}
	}
}

// Without network the command has a network of its own; with it, the one of
// the machine. /dev comes after what is writable.
func TestWrapFlags(t *testing.T) {
	for _, network := range []bool{false, true} {
		cmd, err := wrap(context.Background(), "/usr/bin/bwrap", Policy{Writable: []string{"/"}, Network: network}, "/bin/true", []string{"x"})
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Join(cmd.Args, " ")
		if strings.Contains(args, "--unshare-net") == network ||
			!strings.HasSuffix(args, "--ro-bind / / --bind / / --dev /dev --seccomp 3 -- /bin/true x") {
			t.Errorf("network %v: %s", network, args)
		}
	}
}
