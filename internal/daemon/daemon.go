// Package daemon runs the node as a detached background process tracked by a PID file.
//
// A node holds an exclusive lock on its PID file as long as it lives. The
// lock, not the content of the file, says that a node is running: the kernel
// drops it when the process ends, however it ends, so a file left behind by
// a crash is never taken for a node.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const (
	pollInterval = 50 * time.Millisecond
	// patience is how long a node may take to come up or to go down. It is
	// far more than a node needs: on a busy machine it takes many times as
	// long as usual, and a wait that ends when the node is up costs nothing.
	// A node that ends instead of coming up is noticed at once.
	patience = time.Minute
	// A process that looks whether a node is running has the lock for a
	// moment; a node that starts tries this often before it gives up.
	lockAttempts = 10
	lockInterval = 10 * time.Millisecond
)

// ErrRunning means that another node holds the PID file.
var ErrRunning = errors.New("node is already running")

// flock locks a file; tests replace it to get in the way.
var flock = syscall.Flock

// truncate empties a file; tests replace it, since not every system refuses
// to empty the files that cannot hold content.
var truncate = (*os.File).Truncate

// kill sends a signal to a process; tests replace it to see what is sent.
var kill = syscall.Kill

// Service is a node that runs in the background. Its PID file holds
// "PID PORT" and is locked while it runs; that is how other processes find it.
type Service struct {
	PIDFile string
	LogFile string
	// Env is added to the environment the node is started with.
	Env []string
}

// Running returns the process of the service if it is alive.
func (s Service) Running() (Process, bool) { return Running(s.PIDFile) }

// Start runs exe with args in the background and waits until it is up.
func (s Service) Start(exe string, args ...string) (Process, error) {
	exited, err := spawn(exe, s.LogFile, s.Env, args...)
	if err != nil {
		return Process{}, err
	}
	p, ok := waitRunning(s.PIDFile, patience, exited)
	if !ok {
		return Process{}, fmt.Errorf("node did not start, see %s", s.LogFile)
	}
	return p, nil
}

// Stop stops the service and reports whether it was running.
func (s Service) Stop() (bool, error) { return Stop(s.PIDFile, patience) }

// Process is a running node as recorded in its PID file.
type Process struct {
	PID  int
	Port int
}

// Lease is the hold of a node on its PID file, see Hold.
type Lease struct {
	f    *os.File
	path string
}

// InheritEnv names the descriptor of the PID file a node hands on to the
// process that takes its place, see Lease.Bequeath.
const InheritEnv = "OURO_PID_FD"

// Hold records the current process as the node listening on port and locks
// the PID file at path until the lease is released or the process ends. It
// fails with ErrRunning if another node holds the file.
//
// A node that took the place of another one (see Lease.Bequeath) takes over
// the lock it was handed instead: no other node can start in between.
func Hold(path string, port int) (*Lease, error) {
	if f := inherited(path); f != nil {
		if err := record(f, port); err != nil {
			f.Close()
			return nil, err
		}
		return &Lease{f, path}, nil
	}
	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		if err := lock(f); err != nil {
			f.Close()
			return nil, err
		}
		// A node that stopped in the meantime removed its file: then the
		// lock is on a file nobody finds, and the path needs a new one.
		if !named(f, path) {
			f.Close()
			continue
		}
		if err := record(f, port); err != nil {
			f.Close()
			return nil, err
		}
		return &Lease{f, path}, nil
	}
}

// record writes the current process and port into the locked file f.
func record(f *os.File, port int) error {
	err := truncate(f, 0) // drop what a node before left behind
	if err == nil {
		_, err = f.WriteAt(fmt.Appendf(nil, "%d %d\n", os.Getpid(), port), 0)
	}
	return err
}

// inherited returns the PID file at path as the node before this process
// handed it on, locked, or nil if it handed on none. The variable that
// names it is removed: a process this one starts is not handed it.
func inherited(path string) *os.File {
	v, ok := os.LookupEnv(InheritEnv)
	if !ok {
		return nil
	}
	os.Unsetenv(InheritEnv)
	fd, err := strconv.Atoi(v)
	if err != nil || !same(fd, path) {
		return nil
	}
	f := os.NewFile(uintptr(fd), path)
	// Locked by this process already: locking it again with the same open
	// file is no change.
	if flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}

// same reports whether the descriptor fd is open on the file at path. A
// descriptor that is not is none of the node's, and is left alone.
func same(fd int, path string) bool {
	var open, named syscall.Stat_t
	return fd >= 0 && syscall.Fstat(fd, &open) == nil && syscall.Stat(path, &named) == nil &&
		open.Dev == named.Dev && open.Ino == named.Ino
}

// Release gives up the PID file: it is removed, and the lock is let go.
func (l *Lease) Release() {
	// Removed before the lock is given up, so that nobody locks a file that
	// is about to disappear without noticing.
	os.Remove(l.path)
	l.f.Close()
}

// Bequeath hands the lock on to the program this process is replaced by
// with exec, and gives up the lease: the lock is kept by a descriptor that
// stays open across exec. It returns the variable that tells that program
// which descriptor it is, for its environment; Hold takes it from there.
func (l *Lease) Bequeath() (string, error) {
	defer l.f.Close()
	// A duplicate is open across exec, unlike the file, and holds the same
	// lock: the lock goes when the last descriptor of the file goes.
	fd, err := dup(int(l.f.Fd()))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s=%d", InheritEnv, fd), nil
}

// dup duplicates a descriptor, open across exec; tests replace it to get in
// the way.
var dup = syscall.Dup

// lock takes the exclusive lock on f without waiting for a node to stop.
func lock(f *os.File) error {
	for range lockAttempts {
		err := flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		time.Sleep(lockInterval)
	}
	return ErrRunning
}

// named reports whether f is still the file at path.
func named(f *os.File, path string) bool {
	held, _ := f.Stat()
	current, err := os.Stat(path)
	return err == nil && os.SameFile(held, current)
}

// Running returns the process recorded in path if a node holds the file. A
// file that is not locked was left behind and says nothing: its PID may
// belong to any process by now.
func Running(path string) (Process, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Process{}, false
	}
	defer f.Close() // gives back the lock, should the test get it
	if err := flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		return Process{}, false
	}
	var p Process
	if _, err := fmt.Fscan(f, &p.PID, &p.Port); err != nil || p.PID <= 0 {
		return Process{}, false
	}
	return p, true
}

// spawn starts exe with args in a new session, detached from the terminal,
// with env added to its environment and stdout and stderr appended to
// logPath. exited is closed when the process ends, should this one live to
// see it.
func spawn(exe, logPath string, env []string, args ...string) (exited <-chan struct{}, err error) {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	return done, nil
}

// WaitRunning waits until a node holds path.
func WaitRunning(path string, timeout time.Duration) (Process, bool) {
	return waitRunning(path, timeout, nil)
}

// waitRunning waits until a node holds path, but no longer than the process
// that should become that node lives: exited is closed when it ends.
func waitRunning(path string, timeout time.Duration, exited <-chan struct{}) (Process, bool) {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		if p, ok := Running(path); ok {
			return p, true
		}
		select {
		case <-exited:
			// It is not the node. Another one may be by now: one that was
			// started at the same time and got the file first.
			return Running(path)
		case <-time.After(pollInterval):
		}
	}
	return Process{}, false
}

// Stop terminates the node that holds path, waits until it let go of the
// file and reports whether there was one. Without a node nothing is
// signalled, whatever the file says.
func Stop(path string, timeout time.Duration) (bool, error) {
	p, ok := Running(path)
	if !ok {
		return false, nil
	}
	if err := terminate(p.PID); err != nil {
		return true, err
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if _, ok := Running(path); !ok {
			return true, nil
		}
	}
	return true, errors.New("node did not stop in time")
}

// terminate asks the process pid to end. It is the only place that signals a
// process, and it signals nothing but one process: the PID 0 is the process
// group of the caller, a negative one a group or every process the user may
// signal, and 1 is what the whole system or container hangs on. Whatever a
// caller took for a node, those are never signalled.
func terminate(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal PID %d: that is no node", pid)
	}
	return kill(pid, syscall.SIGTERM)
}
