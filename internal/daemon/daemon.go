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
	"syscall"
	"time"
)

const (
	pollInterval = 50 * time.Millisecond
	// patience is how long a node may take to come up or to go down.
	patience = 5 * time.Second
	// A process that looks whether a node is running has the lock for a
	// moment; a node that starts tries this often before it gives up.
	lockAttempts = 10
	lockInterval = 10 * time.Millisecond
)

// ErrRunning means that another node holds the PID file.
var ErrRunning = errors.New("node is already running")

// flock locks a file; tests replace it to get in the way.
var flock = syscall.Flock

// Service is a node that runs in the background. Its PID file holds
// "PID PORT" and is locked while it runs; that is how other processes find it.
type Service struct {
	PIDFile string
	LogFile string
}

// Running returns the process of the service if it is alive.
func (s Service) Running() (Process, bool) { return Running(s.PIDFile) }

// Start runs exe with args in the background and waits until it is up.
func (s Service) Start(exe string, args ...string) (Process, error) {
	if err := Spawn(exe, s.LogFile, args...); err != nil {
		return Process{}, err
	}
	p, ok := WaitRunning(s.PIDFile, patience)
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

// Hold records the current process as the node listening on port and locks
// the PID file at path until release is called or the process ends. It fails
// with ErrRunning if another node holds the file.
func Hold(path string, port int) (release func(), err error) {
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
		if err = f.Truncate(0); err == nil { // drop what a dead node left behind
			_, err = fmt.Fprintf(f, "%d %d\n", os.Getpid(), port)
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		// Removed before the lock is given up, so that nobody locks a file
		// that is about to disappear without noticing.
		return func() { os.Remove(path); f.Close() }, nil
	}
}

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

// Spawn starts exe with args in a new session, detached from the terminal,
// with stdout and stderr appended to logPath.
func Spawn(exe, logPath string, args ...string) error {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// WaitRunning waits until a node holds path.
func WaitRunning(path string, timeout time.Duration) (Process, bool) {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if p, ok := Running(path); ok {
			return p, true
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
	if err := syscall.Kill(p.PID, syscall.SIGTERM); err != nil {
		return true, err
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if _, ok := Running(path); !ok {
			return true, nil
		}
	}
	return true, errors.New("node did not stop in time")
}
