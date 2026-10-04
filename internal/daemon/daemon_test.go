package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pidFile(t *testing.T) string {
	return filepath.Join(t.TempDir(), "ouro.pid")
}

// exitedPID returns the PID of a process that has already exited and been reaped.
func exitedPID(t *testing.T) int {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestWriteAndReadPIDFile(t *testing.T) {
	path := pidFile(t)
	if err := WritePIDFile(path, 41337); err != nil {
		t.Fatal(err)
	}
	p, ok := Running(path)
	if !ok || p.PID != os.Getpid() || p.Port != 41337 {
		t.Fatalf("got %+v, %v", p, ok)
	}
}

func TestRunningRejectsMissingGarbageAndStale(t *testing.T) {
	stale := pidFile(t)
	os.WriteFile(stale, fmt.Appendf(nil, "%d 41337", exitedPID(t)), 0o600)
	garbage := pidFile(t)
	os.WriteFile(garbage, []byte("nope"), 0o600)

	for name, path := range map[string]string{"missing": pidFile(t), "garbage": garbage, "stale": stale} {
		if p, ok := Running(path); ok {
			t.Errorf("%s: got %+v", name, p)
		}
	}
}

func TestWaitRunning(t *testing.T) {
	path := pidFile(t)
	if _, ok := WaitRunning(path, 100*time.Millisecond); ok {
		t.Fatal("nothing should be running")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		WritePIDFile(path, 1)
	}()
	if p, ok := WaitRunning(path, 2*time.Second); !ok || p.Port != 1 {
		t.Fatalf("got %+v, %v", p, ok)
	}
}

func TestSpawnDetachesAndLogs(t *testing.T) {
	log := filepath.Join(t.TempDir(), "ouro.log")
	if err := Spawn("/bin/sh", log, "-c", "echo spawned; ps -o pgid= -p $$"); err != nil {
		t.Fatal(err)
	}
	var out string
	for range 40 {
		b, _ := os.ReadFile(log)
		if out = string(b); strings.Count(out, "\n") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	lines := strings.Fields(out)
	if len(lines) != 2 || lines[0] != "spawned" {
		t.Fatalf("log = %q", out)
	}
	if myPGID := fmt.Sprint(mustPGID(t)); lines[1] == myPGID {
		t.Fatal("child should run in its own session (and thus process group)")
	}
}

func TestSpawnMissingExecutable(t *testing.T) {
	if err := Spawn("/does/not/exist", filepath.Join(t.TempDir(), "log")); err == nil {
		t.Fatal("want error")
	}
}

func TestStop(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait() // reap, otherwise the zombie counts as alive
	if err := Stop(cmd.Process.Pid, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestStopNotRunning(t *testing.T) {
	if err := Stop(exitedPID(t), time.Second); err == nil {
		t.Fatal("want error")
	}
}

func mustPGID(t *testing.T) int {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		t.Skip("ps not available")
	}
	var pgid int
	fmt.Sscan(string(out), &pgid)
	return pgid
}

// service returns a service whose files are in a fresh directory.
func service(t *testing.T) Service {
	dir := t.TempDir()
	return Service{PIDFile: filepath.Join(dir, "ouro.pid"), LogFile: filepath.Join(dir, "ouro.log")}
}

// A started service is found by its PID file and can always be stopped.
func TestServiceStartAndStop(t *testing.T) {
	s := service(t)
	if _, ok := s.Running(); ok {
		t.Fatal("nothing should be running yet")
	}
	if stopped, err := s.Stop(); stopped || err != nil {
		t.Fatalf("Stop without a node = %v, %v", stopped, err)
	}

	// The node is a sleep that sh leaves behind, as a real node outlives
	// the command that started it.
	p, err := s.Start("/bin/sh", "-c", `sleep 30 & echo "$! 41337" > "$0"`, s.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	if running, ok := s.Running(); !ok || running != p || p.Port != 41337 {
		t.Fatalf("Running = %+v, %v; started %+v", running, ok, p)
	}
	if stopped, err := s.Stop(); !stopped || err != nil {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if _, ok := s.Running(); ok {
		t.Fatal("the node should be gone")
	}
}

func TestServiceStartFails(t *testing.T) {
	t.Parallel() // waits as long as a node may take to come up
	s := service(t)
	if _, err := s.Start("/does/not/exist"); err == nil {
		t.Fatal("a missing executable should fail")
	}
	// A stale PID file is not taken for the new node.
	WritePIDFile(s.PIDFile, 1)
	if _, err := s.Start("/bin/sh", "-c", "exit 0"); err == nil || !strings.Contains(err.Error(), s.LogFile) {
		t.Fatalf("a node that never comes up: %v", err)
	}
}

func TestSpawnWithoutLog(t *testing.T) {
	if err := Spawn("/bin/sh", filepath.Join(t.TempDir(), "missing", "log"), "-c", "exit 0"); err == nil {
		t.Fatal("want error")
	}
}

// A process that ignores the request to stop is reported, not waited for.
func TestStopStubbornProcess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `trap "" TERM; echo ready; exec sleep 30`)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	if _, err := out.Read(make([]byte, 16)); err != nil { // the trap is set
		t.Fatal(err)
	}
	if err := Stop(cmd.Process.Pid, 200*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not exit in time") {
		t.Fatalf("got %v", err)
	}
}
