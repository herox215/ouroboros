package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary is also the node of these tests: called as "node" or
// "stubborn" with a PID file, it holds the file like a node does. Called as
// "pgid" it says that it was spawned and in which process group it runs.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && (os.Args[1] == "node" || os.Args[1] == "stubborn") {
		os.Exit(fakeNode(os.Args[2], os.Args[1] == "stubborn"))
	}
	if len(os.Args) == 3 && (os.Args[1] == "bequeath" || os.Args[1] == "heir") {
		os.Exit(succession(os.Args[2], os.Args[1] == "heir"))
	}
	if len(os.Args) == 2 && os.Args[1] == "pgid" {
		fmt.Printf("spawned\n%d\n", syscall.Getpgrp())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeNode holds path until it is asked to stop; a stubborn one does not listen.
func fakeNode(path string, stubborn bool) int {
	term := make(chan os.Signal, 1)
	if stubborn {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(term, syscall.SIGTERM)
	}
	lease, err := Hold(path, 41337)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer lease.Release()
	select {
	case <-term:
	case <-time.After(30 * time.Second):
	}
	return 0
}

// succession holds path, and hands it on to the heir that this process is
// replaced by: the heir holds it as the node on port 41338 until it is
// asked to stop.
func succession(path string, heir bool) int {
	port := 41337
	if heir {
		port = 41338
	}
	lease, err := Hold(path, port)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	if heir {
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		defer lease.Release()
		select {
		case <-term:
		case <-time.After(30 * time.Second):
		}
		return 0
	}
	env, err := lease.Bequeath()
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Println(syscall.Exec(os.Args[0], []string{os.Args[0], "heir", path}, append(os.Environ(), env)))
	return 1
}

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

// bystander starts a process that has nothing to do with a node and returns
// its PID and a function that reports whether it is still running.
func bystander(t *testing.T) (pid int, alive func() bool) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	return cmd.Process.Pid, func() bool {
		select {
		case <-exited:
			return false
		case <-time.After(200 * time.Millisecond): // more than a signal needs
			return true
		}
	}
}

func TestHoldAndRunning(t *testing.T) {
	path := pidFile(t)
	os.WriteFile(path, []byte("1 2 and what a dead node left behind"), 0o600)
	lease, err := Hold(path, 41337)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := Running(path)
	if !ok || p.PID != os.Getpid() || p.Port != 41337 {
		t.Fatalf("got %+v, %v", p, ok)
	}
	if b, _ := os.ReadFile(path); string(b) != fmt.Sprintf("%d 41337\n", os.Getpid()) {
		t.Fatalf("PID file = %q", b)
	}

	// The file belongs to one node at a time.
	if _, err := Hold(path, 1); !errors.Is(err, ErrRunning) {
		t.Fatalf("second node: got %v", err)
	}
	if p, ok := Running(path); !ok || p.Port != 41337 {
		t.Fatalf("the node lost its file: %+v, %v", p, ok)
	}

	lease.Release()
	if _, ok := Running(path); ok {
		t.Fatal("a released file should not count as running")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a released file should be removed")
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

// What a locked file says is only believed if it names a process.
func TestRunningRejectsHeldGarbage(t *testing.T) {
	path := pidFile(t)
	lease, err := Hold(path, 41337)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, content := range []string{"", "nope", "0 41337", "-1 41337"} {
		os.WriteFile(path, []byte(content), 0o600)
		if p, ok := Running(path); ok {
			t.Errorf("%q: got %+v", content, p)
		}
		if stopped, err := Stop(path, time.Second); stopped || err != nil {
			t.Errorf("%q: Stop = %v, %v", content, stopped, err)
		}
	}
}

// A PID file that outlived its node is not a node, even if the PID belongs
// to a process again: that process is never signalled.
func TestStalePIDFileOfLiveProcess(t *testing.T) {
	pid, alive := bystander(t)
	path := pidFile(t)
	os.WriteFile(path, fmt.Appendf(nil, "%d 41337\n", pid), 0o600)

	if p, ok := Running(path); ok {
		t.Fatalf("Running = %+v", p)
	}
	if stopped, err := Stop(path, time.Second); stopped || err != nil {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if !alive() {
		t.Fatal("an unrelated process was stopped")
	}

	// A node takes the file over.
	lease, err := Hold(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if p, ok := Running(path); !ok || p.PID != os.Getpid() || p.Port != 1 {
		t.Fatalf("got %+v, %v", p, ok)
	}
}

func TestHoldReportsErrors(t *testing.T) {
	if _, err := Hold(filepath.Join(t.TempDir(), "missing", "ouro.pid"), 1); err == nil {
		t.Error("a missing directory should fail")
	}
	// A file that is locked but cannot be written fails.
	full := errors.New("full")
	truncate = func(*os.File, int64) error { return full }
	if _, err := Hold(pidFile(t), 1); !errors.Is(err, full) {
		t.Errorf("a file that cannot be written: got %v", err)
	}
	truncate = (*os.File).Truncate

	refused := errors.New("no lock")
	flock = func(int, int) error { return refused }
	defer func() { flock = syscall.Flock }()
	path := pidFile(t)
	if _, err := Hold(path, 1); !errors.Is(err, refused) {
		t.Errorf("without a lock: got %v", err)
	}
	writeStale(t, path)
	if p, ok := Running(path); ok {
		t.Errorf("without a lock: Running = %+v", p)
	}
}

// writeStale leaves a PID file behind as a node does that was killed.
func writeStale(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d 41337\n", os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A process that only looks whether a node is running does not keep a node
// from starting.
func TestHoldWaitsForALook(t *testing.T) {
	looks := 0
	flock = func(fd, how int) error {
		if looks++; looks <= 2 {
			return syscall.EWOULDBLOCK
		}
		return syscall.Flock(fd, how)
	}
	defer func() { flock = syscall.Flock }()
	lease, err := Hold(pidFile(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if looks != 3 {
		t.Fatalf("locked %d times", looks)
	}
}

// A node that gets the lock of a file its predecessor just removed does not
// end up with a file nobody finds.
func TestHoldFileRemovedMeanwhile(t *testing.T) {
	path := pidFile(t)
	removed := false
	flock = func(fd, how int) error {
		if !removed {
			removed = true
			os.Remove(path) // the predecessor stops between open and lock
		}
		return syscall.Flock(fd, how)
	}
	defer func() { flock = syscall.Flock }()
	lease, err := Hold(path, 41337)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	flock = syscall.Flock
	if p, ok := Running(path); !ok || p.Port != 41337 {
		t.Fatalf("got %+v, %v", p, ok)
	}
}

func TestWaitRunning(t *testing.T) {
	path := pidFile(t)
	if _, ok := WaitRunning(path, 100*time.Millisecond); ok {
		t.Fatal("nothing should be running")
	}
	released := make(chan *Lease, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		lease, _ := Hold(path, 1)
		released <- lease
	}()
	defer func() { (<-released).Release() }()
	if p, ok := WaitRunning(path, 2*time.Second); !ok || p.Port != 1 {
		t.Fatalf("got %+v, %v", p, ok)
	}
}

func TestSpawnDetachesAndLogs(t *testing.T) {
	log := filepath.Join(t.TempDir(), "ouro.log")
	exited, err := spawn(os.Args[0], log, nil, "pgid")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the end of the process should be noticed")
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
	if myPGID := fmt.Sprint(syscall.Getpgrp()); lines[1] == myPGID {
		t.Fatal("child should run in its own session (and thus process group)")
	}
}

func TestSpawnMissingExecutable(t *testing.T) {
	if _, err := spawn("/does/not/exist", filepath.Join(t.TempDir(), "log"), nil); err == nil {
		t.Fatal("want error")
	}
}

func TestStopNotRunning(t *testing.T) {
	if stopped, err := Stop(pidFile(t), time.Second); stopped || err != nil {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
}

// A node whose process cannot be signalled is reported.
func TestStopUnreachableProcess(t *testing.T) {
	path := pidFile(t)
	lease, err := Hold(path, 41337)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	os.WriteFile(path, fmt.Appendf(nil, "%d 41337\n", exitedPID(t)), 0o600)
	if stopped, err := Stop(path, time.Second); !stopped || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
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

	p, err := s.Start(os.Args[0], "node", s.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	if running, ok := s.Running(); !ok || running != p || p.Port != 41337 || p.PID == os.Getpid() {
		t.Fatalf("Running = %+v, %v; started %+v", running, ok, p)
	}
	if stopped, err := s.Stop(); !stopped || err != nil {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if _, ok := s.Running(); ok {
		t.Fatal("the node should be gone")
	}
}

// A node that was killed leaves its PID file behind. It counts as stopped at
// once, and the next node starts over the file.
func TestServiceSurvivesKilledNode(t *testing.T) {
	s := service(t)
	p, err := s.Start(os.Args[0], "node", s.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(p.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if _, ok := s.Running(); !ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if running, ok := s.Running(); ok {
		t.Fatalf("a killed node counts as running: %+v", running)
	}
	if _, err := os.Stat(s.PIDFile); err != nil {
		t.Fatalf("the killed node should leave its PID file behind: %v", err)
	}
	if stopped, err := s.Stop(); stopped || err != nil {
		t.Fatalf("Stop after a kill = %v, %v", stopped, err)
	}

	again, err := s.Start(os.Args[0], "node", s.PIDFile)
	if err != nil || again.PID == p.PID {
		t.Fatalf("restart: %+v, %v", again, err)
	}
	if stopped, err := s.Stop(); !stopped || err != nil {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
}

func TestServiceStartFails(t *testing.T) {
	s := service(t)
	if _, err := s.Start("/does/not/exist"); err == nil {
		t.Fatal("a missing executable should fail")
	}
	// A stale PID file is not taken for the new node. A node that ends
	// instead of coming up is not waited for.
	writeStale(t, s.PIDFile)
	start := time.Now()
	if _, err := s.Start("/bin/sh", "-c", "exit 0"); err == nil || !strings.Contains(err.Error(), s.LogFile) {
		t.Fatalf("a node that never comes up: %v", err)
	}
	if time.Since(start) >= patience {
		t.Fatal("a node that ended was waited for")
	}
	// What it said of why, in the last line of the log, is told.
	if _, err := s.Start("/bin/sh", "-c", "echo '✗ access \"x\": no such access' >&2"); err == nil ||
		err.Error() != `node did not start: access "x": no such access (see `+s.LogFile+`)` {
		t.Fatalf("a node that said why: %v", err)
	}
}

// Two nodes started at once: one gets the file, the other one ends. Whoever
// started the one that ended finds the node that runs.
func TestServiceStartFindsTheOtherNode(t *testing.T) {
	s := service(t)
	lease, err := Hold(s.PIDFile, 41337)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	exited := make(chan struct{})
	close(exited)
	// Looked at through another path, the file is not held on the first
	// look: only the look after the end of the process finds the node.
	looks := 0
	held := flock
	flock = func(fd, how int) error {
		if looks++; looks == 1 {
			return nil
		}
		return held(fd, how)
	}
	defer func() { flock = held }()
	if p, ok := waitRunning(s.PIDFile, patience, exited); !ok || p.PID != os.Getpid() || looks != 2 {
		t.Fatalf("got %+v, %v after %d looks", p, ok, looks)
	}
}

// Only a single process is ever signalled: not the process group of the
// caller (0), not every process of the user (-1), not a group, not init.
func TestNothingButOneProcessIsSignalled(t *testing.T) {
	var signalled []int
	kill = func(pid int, _ syscall.Signal) error {
		signalled = append(signalled, pid)
		return nil
	}
	defer func() { kill = syscall.Kill }()

	for _, pid := range []int{1, 0, -1, -2, -os.Getpid()} {
		if err := terminate(pid); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("refusing to signal PID %d", pid)) {
			t.Errorf("PID %d: got %v", pid, err)
		}
	}
	// Also when a file that is held names it: Running believes a PID of 1.
	path := pidFile(t)
	lease, err := Hold(path, 41337)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	os.WriteFile(path, []byte("1 41337\n"), 0o600)
	if stopped, err := Stop(path, time.Second); !stopped || err == nil || !strings.Contains(err.Error(), "refusing to signal PID 1") {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if len(signalled) != 0 {
		t.Fatalf("signalled %v", signalled)
	}
	if err := terminate(2); err != nil || len(signalled) != 1 || signalled[0] != 2 {
		t.Fatalf("a single process should be signalled: %v, %v", signalled, err)
	}
}

func TestSpawnWithoutLog(t *testing.T) {
	if _, err := spawn("/bin/sh", filepath.Join(t.TempDir(), "missing", "log"), nil, "-c", "exit 0"); err == nil {
		t.Fatal("want error")
	}
}

// A node that ignores the request to stop is reported, not waited for.
func TestStopStubbornNode(t *testing.T) {
	s := service(t)
	p, err := s.Start(os.Args[0], "stubborn", s.PIDFile)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(p.PID, syscall.SIGKILL)
	if stopped, err := Stop(s.PIDFile, 200*time.Millisecond); !stopped || err == nil || !strings.Contains(err.Error(), "did not stop in time") {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
}

// A node hands its PID file on to the program it is replaced by with exec:
// the lock is held all the time, and the heir records itself in the file.
func TestBequeathAcrossExec(t *testing.T) {
	path := pidFile(t)
	cmd := exec.Command(os.Args[0], "bequeath", path)
	out, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
		if p, ok := Running(path); ok && p.Port == 41338 {
			if p.PID != cmd.Process.Pid {
				t.Fatalf("the heir is %d, not %d", p.PID, cmd.Process.Pid)
			}
			break
		}
		if time.Now().After(deadline) {
			said, _ := os.ReadFile(out.Name())
			t.Fatalf("the heir does not hold the file: %s", said)
		}
	}
	// Nobody else gets the file in the meantime.
	if _, err := Hold(path, 1); !errors.Is(err, ErrRunning) {
		t.Fatalf("got %v", err)
	}
	if stopped, err := Stop(path, patience); !stopped || err != nil {
		t.Fatalf("stop: %v, %v", stopped, err)
	}
}

// In the process that takes over, the lease is the one handed on: the file is
// the node's, and its process and port are recorded anew.
func TestHoldInherits(t *testing.T) {
	path := pidFile(t)
	lease, err := Hold(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	env, err := lease.Bequeath()
	k, v, _ := strings.Cut(env, "=")
	if err != nil || k != InheritEnv {
		t.Fatalf("bequeathed %q, %v", env, err)
	}
	t.Setenv(k, v)
	heir, err := Hold(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv(InheritEnv); ok {
		t.Fatal("a process started later should not be handed the file")
	}
	if p, ok := Running(path); !ok || p.Port != 2 || p.PID != os.Getpid() {
		t.Fatalf("running %+v, %v", p, ok)
	}
	heir.Release()
	if _, ok := Running(path); ok {
		t.Fatal("released")
	}
}

// What is not handed on as a PID file of a node is left alone, and the file
// is held as usual: here it is held by another file already.
func TestHoldDoesNotInheritWhatIsNone(t *testing.T) {
	path := pidFile(t)
	lease, err := Hold(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	other, err := os.Create(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	unlocked, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlocked.Close()
	// What is handed on is the new node's: it may close it.
	handed, err := syscall.Dup(int(unlocked.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{
		"no number":                       "x",
		"no descriptor":                   "-1",
		"another file":                    fmt.Sprint(other.Fd()),
		"the file, open without its lock": fmt.Sprint(handed),
	} {
		t.Setenv(InheritEnv, v)
		if _, err := Hold(path, 2); !errors.Is(err, ErrRunning) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := other.Stat(); err != nil {
		t.Fatalf("another file should be left alone: %v", err)
	}
}

// A lease that cannot be handed on, or a file handed on that cannot be
// written, says so.
func TestSuccessionFails(t *testing.T) {
	path := pidFile(t)
	lease, err := Hold(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("too many open files")
	dup = func(int) (int, error) { return 0, refused }
	_, err = lease.Bequeath()
	dup = syscall.Dup
	if !errors.Is(err, refused) {
		t.Fatalf("got %v", err)
	}
	if _, ok := Running(path); ok {
		t.Fatal("a lease that is not handed on is given up")
	}

	lease, err = Hold(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := lease.Bequeath()
	_, v, _ := strings.Cut(env, "=")
	t.Setenv(InheritEnv, v)
	full := errors.New("full")
	truncate = func(*os.File, int64) error { return full }
	defer func() { truncate = (*os.File).Truncate }()
	if _, err := Hold(path, 2); !errors.Is(err, full) {
		t.Fatalf("got %v", err)
	}
}
