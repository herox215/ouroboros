package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/openai/openaitest"
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

	started := ouro("\n"+openaitest.Key+"\n2\ne2e-node\n", "start")
	expect(started, "API endpoint", "Available models", "online", "gpt-4o-mini")
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
	if _, err := fmt.Sscan(string(record), &pid, &port); err != nil {
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

	cmd := exec.Command(bin, "fly")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("unknown command should exit with 1, got %v", err)
	}
	expect(string(out), "✗ unknown command")
}

func freeUDPPort(t *testing.T) int {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}
