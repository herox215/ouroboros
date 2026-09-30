// Package daemon runs the node as a detached background process tracked by a PID file.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const pollInterval = 50 * time.Millisecond

// Process is a running node as recorded in its PID file.
type Process struct {
	PID  int
	Port int
}

// WritePIDFile records the current process as a node listening on port.
func WritePIDFile(path string, port int) error {
	return os.WriteFile(path, fmt.Appendf(nil, "%d %d\n", os.Getpid(), port), 0o600)
}

// Running returns the process recorded in path if it is still alive.
func Running(path string) (Process, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Process{}, false
	}
	var p Process
	if _, err := fmt.Sscan(string(b), &p.PID, &p.Port); err != nil || !alive(p.PID) {
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

// WaitRunning waits until path records a live process.
func WaitRunning(path string, timeout time.Duration) (Process, bool) {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if p, ok := Running(path); ok {
			return p, true
		}
	}
	return Process{}, false
}

// Stop terminates pid and waits for it to exit.
func Stop(pid int, timeout time.Duration) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if !alive(pid) {
			return nil
		}
	}
	return errors.New("process did not exit in time")
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
