package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
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
	if _, err := wrap(context.Background(), "/usr/bin/bwrap", nil, "true", nil); !errors.Is(err, broken) {
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
