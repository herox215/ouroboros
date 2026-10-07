package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"ouroboros/internal/openai/openaitest"
	"ouroboros/plugin"
)

// TestEndToEnd builds the real binary and drives it like a user would.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ouro")
	build := []string{"build", "-o", bin}
	coverDir := os.Getenv("OURO_E2E_COVERDIR") // set by: make cover
	if coverDir != "" {
		build = append(build, "-cover", "-coverpkg=ouroboros/...")
	}
	if out, err := exec.Command("go", append(build, ".")...).CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	env := append(os.Environ(),
		"OURO_HOME="+filepath.Join(dir, "home"),
		"OURO_PORT=0",
		fmt.Sprintf("OURO_DISCO_PORT=%d", freeUDPPort(t)),
		"OPENAI_BASE_URL="+openaitest.NewServer(t),
		"OPENAI_API_KEY=",
		"GOCOVERDIR="+coverDir,
	)
	ouro := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env, cmd.Stdin = env, strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ouro %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Cleanup(func() { ouro("", "stop") })

	expect := func(out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Fatalf("output %q does not contain %q", out, w)
			}
		}
	}

	// The provider (Enter: the one for OpenAI-compatible APIs), then what it
	// asks, the endpoint and the key, then the model and the name.
	started := ouro("\n\n"+openaitest.Key+"\n2\ne2e-node\n", "start")
	expect(started, "LLM endpoint", "Available models", "online", "gpt-4o-mini")
	// The ID is the fingerprint of the node's key: the same wherever it shows.
	id := regexp.MustCompile(`ouroboros ([0-9a-f]{12}) \(e2e-node\)`).FindStringSubmatch(started)
	if id == nil {
		t.Fatalf("no node ID in %q", started)
	}
	expect(ouro("", "status"), "● running", "id="+id[1], "name=e2e-node")
	expect(ouro("", "list"), id[1], "e2e-node", "gpt-4o-mini")
	expect(ouro("", "ask", "e2e-node", "test"), "[gpt-4o-mini] turns=1 last=test")
	expect(ouro("", "chat", "hello"), "[gpt-4o-mini] turns=1 last=hello")
	expect(ouro("", "start"), "already running")
	expect(ouro("", "stop"), "stopped")
	expect(ouro("", "status"), "not running")

	// Second start reuses the saved configuration without asking.
	expect(ouro("", "start"), "online", id[1])
	expect(ouro("", "stop"), "stopped")

	// A node that is killed leaves its PID file behind. That is no node, even
	// when the PID belongs to another process by now, which is left alone.
	pidFile := filepath.Join(dir, "home", "ouro.pid")
	expect(ouro("", "start"), "online")
	var pid, port int
	record, _ := os.ReadFile(pidFile)
	// Only a single process is killed below: a PID of 0 or less is a group.
	if _, err := fmt.Sscan(string(record), &pid, &port); err != nil || pid <= 1 {
		t.Fatalf("PID file %q: %v", record, err)
	}
	// What "ask" got its answer over is TLS 1.3; the node speaks nothing else.
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{InsecureSkipVerify: true})
	if err != nil || conn.ConnectionState().Version != tls.VersionTLS13 {
		t.Fatalf("the node should speak TLS 1.3: %v", err)
	}
	conn.Close()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/.well-known/agent-card.json", port))
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain HTTP should be refused: %v, %v", resp, err)
	}
	resp.Body.Close()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		bystander.Wait()
		close(exited)
	}()
	defer bystander.Process.Kill()
	os.WriteFile(pidFile, fmt.Appendf(nil, "%d %d\n", bystander.Process.Pid, port), 0o600)
	expect(ouro("", "status"), "not running")
	expect(ouro("", "stop"), "not running")
	select {
	case <-exited:
		t.Fatal("stop signalled an unrelated process")
	case <-time.After(200 * time.Millisecond): // more than a signal needs
	}
	expect(ouro("", "start"), "online")
	expect(ouro("", "status"), "● running")
	expect(ouro("", "stop"), "stopped")

	log, _ := os.ReadFile(filepath.Join(dir, "home", "ouro.log"))
	expect(string(log), "listening on", "POST /", "stopped")

	// What fails says why and exits with 1: a command, and the environment.
	for want, c := range map[string]struct{ env, arg string }{
		"✗ unknown command":                   {"OURO_PORT=0", "fly"},
		`✗ OURO_PORT: "http" is not a number`: {"OURO_PORT=http", "status"},
	} {
		cmd := exec.Command(bin, c.arg)
		cmd.Env = append(slices.Clone(env), c.env)
		out, err := cmd.CombinedOutput()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("ouro %s with %s should exit with 1, got %v", c.arg, c.env, err)
		}
		expect(string(out), want)
	}

	// At a terminal the API key is asked for without showing it.
	tty := newTerminal(t)
	env = append(env, "OURO_HOME="+filepath.Join(dir, "typed"))
	cmd := exec.Command(bin, "start")
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, tty.user, tty.user, tty.user
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tty.await("Provider [number, Enter = 1]: ")
	tty.typeLine("")
	tty.await("LLM endpoint")
	tty.typeLine("")
	tty.await("API key (Enter for none): ")
	// Typed before the echo is off, the key would show like on any terminal:
	// a user is slower than that, the test waits.
	tty.awaitEcho(false)
	tty.typeLine(openaitest.Key)
	tty.await("Choose model")
	tty.typeLine("2")
	tty.await("Node name")
	tty.typeLine("typed-node")
	tty.await("online")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("ouro start at a terminal: %v\n%s", err, tty.shown())
	}
	expect(ouro("", "stop"), "stopped")
	expect(tty.shown(), "Available models", "gpt-4o-mini", "typed-node")
	if strings.Contains(tty.shown(), openaitest.Key) {
		t.Fatalf("the API key was shown on the terminal: %q", tty.shown())
	}
	tty.awaitEcho(true) // the terminal is left as it was found
}

// terminal is a pseudo-terminal: ouro runs at the user's end, the test types
// and reads at the other one.
type terminal struct {
	t        *testing.T
	keyboard *os.File // the end of the test
	user     *os.File // the end a program at the terminal has

	mu     sync.Mutex
	screen []byte
}

// patience is how long ouro may take for a step at the terminal on a busy machine.
const patience = 2 * time.Minute

func newTerminal(t *testing.T) *terminal {
	t.Helper()
	keyboard, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { keyboard.Close() })
	name, err := unlockTerminal(keyboard)
	if err != nil {
		t.Fatal(err)
	}
	user, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { user.Close() })
	tty := &terminal{t: t, keyboard: keyboard, user: user}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := keyboard.Read(buf)
			tty.mu.Lock()
			tty.screen = append(tty.screen, buf[:n]...)
			tty.mu.Unlock()
			if err != nil {
				return // closed when the test ends
			}
		}
	}()
	return tty
}

// shown is everything the terminal showed so far.
func (tty *terminal) shown() string {
	tty.mu.Lock()
	defer tty.mu.Unlock()
	return string(tty.screen)
}

// await waits until the terminal shows text.
func (tty *terminal) await(text string) {
	tty.t.Helper()
	for deadline := time.Now().Add(patience); !strings.Contains(tty.shown(), text); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			tty.t.Fatalf("the terminal does not show %q: %q", text, tty.shown())
		}
	}
}

// awaitEcho waits until the terminal shows what is typed, or no longer does.
func (tty *terminal) awaitEcho(on bool) {
	tty.t.Helper()
	for deadline := time.Now().Add(patience); ; time.Sleep(10 * time.Millisecond) {
		var state syscall.Termios
		if err := ioctl(tty.user, getTermios, unsafe.Pointer(&state)); err != nil {
			tty.t.Fatal(err)
		}
		if (state.Lflag&syscall.ECHO != 0) == on {
			return
		}
		if time.Now().After(deadline) {
			tty.t.Fatalf("the echo of the terminal should be on: %v", on)
		}
	}
}

// typeLine types a line as the user.
func (tty *terminal) typeLine(line string) {
	tty.t.Helper()
	if _, err := tty.keyboard.WriteString(line + "\n"); err != nil {
		tty.t.Fatal(err)
	}
}

// ioctl controls the device behind f.
func ioctl(f *os.File, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

func freeUDPPort(t *testing.T) int {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// The front end that asks to be started anew is: the binary it names runs
// in place of ouro, with its arguments and the environment it hands over.
func TestRestart(t *testing.T) {
	var ran []string
	old := execute
	execute = func(path string, args, env []string) error {
		ran = append(append([]string{path}, args...), env...)
		return errors.New("exec failed")
	}
	t.Cleanup(func() { execute = old })

	other := errors.New("other")
	if err := restart(other); err != other || ran != nil {
		t.Fatalf("got %v, ran %v", err, ran)
	}
	err := restart(fmt.Errorf("tui: %w", &plugin.Restarting{Exe: "/new/ouro", Env: []string{"A=1"}}))
	if want := append(append([]string{"/new/ouro"}, os.Args...), "A=1"); err == nil || err.Error() != "exec failed" || !slices.Equal(ran, want) {
		t.Fatalf("got %v, ran %v", err, ran)
	}
}
