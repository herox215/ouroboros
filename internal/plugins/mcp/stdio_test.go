package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
)

// TestStdioServer is the server of the other tests, in a program of its own:
// the same test binary, started for the one test.
func TestStdioServer(t *testing.T) {
	if os.Getenv("OURO_MCP_HELPER") != "1" {
		t.Skip("the helper of the stdio tests")
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "helper", Version: "1"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "pid", Description: "the PID of the server"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strconv.Itoa(os.Getpid())}}}, nil, nil
		})
	// What such a wrapper leaves behind in the real world, npx and uvx: a
	// process of its own, which a signal to the wrapper alone does not reach.
	child := exec.Command("sleep", "60")
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "child", Description: "start a child and give its PID"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
			if child.Process == nil {
				if err := child.Start(); err != nil {
					return nil, nil, err
				}
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strconv.Itoa(child.Process.Pid)}}}, nil, nil
		})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

// TestStdioAndGroup: a local server is a program that is started, spoken to,
// and ended with its group when the connection ends.
func TestStdioAndGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the group of a program is a POSIX signal")
	}
	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	st := &server{name: "helper", cfg: config.Server{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestStdioServer$", "-test.timeout=60s"},
		Env:     map[string]string{"OURO_MCP_HELPER": "1"},
	}}
	tools, err := m.connect(context.Background(), st, false)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"child", "pid"}) {
		t.Fatalf("tools = %v", names)
	}
	out, err := m.call(context.Background(), st, "pid", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("the server's answer %q is no PID: %v", out, err)
	}
	out, err = m.call(context.Background(), st, "child", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("the server's answer %q is no PID: %v", out, err)
	}
	st.drop()
	// The wrapper and the process it started are gone: a signal to the wrapper
	// alone leaves the child behind.
	for _, id := range []int{pid, child} {
		waitGone(t, id)
	}
}

// waitGone waits for the process to be gone, and kills it when it does not
// come to that: no test leaves a process behind.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the process %d still runs after its connection ended: %v", pid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
