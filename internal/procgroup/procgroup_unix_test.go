//go:build unix

package procgroup

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestCancelEndsTheGroup checks that the end of the context takes what the
// command started along with it, not only the command itself.
func TestCancelEndsTheGroup(t *testing.T) {
	dir := t.TempDir()
	// What the command starts in the background holds a FIFO open while it
	// runs: reading the FIFO ends once nothing of the group is left.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		f, err := os.Open(fifo)
		if err == nil {
			_, err = io.ReadAll(f)
			f.Close()
		}
		closed <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sh -c 'echo started; exec sleep 60' 3>fifo &\nwait")
	cmd.Dir = dir
	Detach(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The child writes once it holds the FIFO.
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "started\n" {
		t.Fatalf("the command did not start its child: %q, %v", line, err)
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("a command killed by the end of its context ended well")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("what the command started still runs")
	}
}

// A command that ended by itself is no error to kill: its group is gone.
func TestKillAfterTheEnd(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	Detach(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := Kill(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after the end: %v", err)
	}
}

// A command that was not started, or one with a PID of 1 or less, has no
// group that can be signalled. Kill is only called for the first: were the
// check of the PID broken, a test that kills the group of PID 0 or 1 would
// kill its own group or every process of the user.
func TestNoGroupBelowPID2(t *testing.T) {
	if err := Kill(exec.Command("true")); err == nil {
		t.Error("a command that was not started was killed")
	}
	for _, pid := range []int{-1, 0, 1} {
		cmd := exec.Command("true")
		cmd.Process = &os.Process{Pid: pid}
		if _, err := group(cmd); err == nil {
			t.Errorf("PID %d has a group", pid)
		}
	}
	cmd := exec.Command("true")
	cmd.Process = &os.Process{Pid: 2}
	if pgid, err := group(cmd); err != nil || pgid != 2 {
		t.Errorf("group of PID 2: %d, %v", pgid, err)
	}
}
