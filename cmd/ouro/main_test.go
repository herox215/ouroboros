package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

	expect(ouro(openaitest.Key+"\n2\ne2e-node\n", "start"), "Available models", "online", "gpt-4o-mini")
	expect(ouro("", "status"), "● running", "name=e2e-node")
	expect(ouro("", "list"), "e2e-node", "gpt-4o-mini")
	expect(ouro("", "chat", "e2e-node", "test"), "[gpt-4o-mini] turns=1 last=test")
	expect(ouro("", "start"), "already running")
	expect(ouro("", "stop"), "stopped")
	expect(ouro("", "status"), "not running")

	// Second start reuses the saved configuration without asking.
	expect(ouro("", "start"), "online")
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
