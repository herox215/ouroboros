package node

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// socketEnv names the descriptor of the socket a node hands on to the
// process that takes its place, see Succession.
const socketEnv = "OURO_SOCKET_FD"

// listenLocal opens the socket at path for the front ends of the owner. Its
// directory is the user's alone, and so is the socket. A node that took the
// place of another one takes over the socket it was handed.
func listenLocal(path string) (*net.UnixListener, error) {
	if ln := inheritedSocket(path); ln != nil {
		return ln, nil
	}
	if err := private(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// A socket that is left behind is of a node that is gone: this one
	// holds the PID file.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// private makes sure that the directory dir is there and that nobody but the
// user can enter it: one of another user, or a link to one, is refused.
func private(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || owner(info) != os.Getuid() {
		return fmt.Errorf("%s is not a directory of this user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// owner returns the user who owns the file of info.
func owner(info os.FileInfo) int { return int(info.Sys().(*syscall.Stat_t).Uid) }

// inheritedSocket returns the socket at path as the node before this process
// handed it on, or nil if it handed on none. The variable that names it is
// removed: a process this one starts is not handed it.
func inheritedSocket(path string) *net.UnixListener {
	v, ok := os.LookupEnv(socketEnv)
	if !ok {
		return nil
	}
	os.Unsetenv(socketEnv)
	fd, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return nil // no socket: none of the node's, and left alone
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close() // the listener has a descriptor of its own
	ln, err := net.FileListener(f)
	if err != nil {
		return nil
	}
	unix, ok := ln.(*net.UnixListener)
	if !ok || unix.Addr().String() != path {
		ln.Close()
		return nil
	}
	unix.SetUnlinkOnClose(true) // it is this node's now
	return unix
}

// bequeathSocket keeps the socket ln open for the program this process is
// replaced by with exec, and returns the descriptor it is open at there. The
// file of the socket stays when ln is closed.
func bequeathSocket(ln *net.UnixListener) (int, error) {
	ln.SetUnlinkOnClose(false)
	f, err := ln.File()
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// A duplicate is open across exec, unlike the file.
	return dup(int(f.Fd()))
}

// chmod changes the mode of a file; tests replace it to get in the way.
var chmod = os.Chmod

// dup duplicates a descriptor, open across exec; tests replace it to get in
// the way.
var dup = syscall.Dup
