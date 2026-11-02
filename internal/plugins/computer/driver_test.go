package computer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// script is a program for launch to start as cua-driver: a shell script
// with body, which is started with mcp.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cua-driver")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$*\" = \""+strings.Join(arguments(runtime.GOOS), " ")+"\" ] || exit 2\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// greeting is what cua-driver answers the initialize of the SDK with, as
// far as the SDK needs it.
const greeting = `{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"cua-driver","version":"1"}}`

// server is a script body that answers the greeting of the SDK on standard
// input, and ends with it.
func server() string {
	return `while read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
  *'"id":'*) printf '{"jsonrpc":"2.0","id":%s,"result":` + greeting + `}\n' "$id" ;;
  esac
done`
}

// launch starts the program, greets it and tells it to send no telemetry;
// it ends with its input closed, or is killed.
func TestLaunch(t *testing.T) {
	ctx := context.Background()
	env := filepath.Join(t.TempDir(), "env")
	d, err := launch(ctx, script(t, `printf '%s %s %s' "$CUA_DRIVER_RS_TELEMETRY_ENABLED" "$CUA_TELEMETRY_ENABLED" "$CUA_DRIVER_RS_ENABLE_WAYLAND" > '`+env+`'
`+server()))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(env); err != nil || string(b) != "false false 1" {
		t.Fatalf("telemetry: %q, %v", b, err)
	}
	d.end()
	if !d.closed() {
		t.Fatal("ended, but not closed")
	}

	// One that does not end with its input is killed.
	old := closeTimeout
	closeTimeout = 50 * time.Millisecond
	defer func() { closeTimeout = old }()
	d, err = launch(ctx, script(t, server()+`
trap '' TERM HUP
exec sleep 60
`))
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	d.end()
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("ended after %v", took)
	}

	// What it started ends with it, not only the program itself: the child
	// holds a FIFO open, which ends only once the group is gone.
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		f, err := os.Open(fifo)
		if err == nil {
			_, err = f.Read(make([]byte, 1))
			f.Close()
		}
		closed <- err
	}()
	d, err = launch(ctx, script(t, "sh -c 'exec 3>"+fifo+"; sleep 60' >&- &\n"+server()+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	d.end()
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("what it started said something")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("what the driver started is still running")
	}

	// One that stops answering but goes on is not waited for to say why:
	// what it wrote until then has to do.
	began = time.Now()
	_, err = launch(ctx, script(t, `exec >&-
trap '' TERM HUP
exec sleep 60
`))
	if err == nil || !strings.HasPrefix(err.Error(), "cua-driver does not start: cua-driver ended") {
		t.Fatalf("silent: %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("gave up after %v", took)
	}

	// One that fails says why, with the last of what it wrote.
	_, err = launch(ctx, script(t, `head -c 3000 /dev/zero | tr '\0' x >&2
echo " no display" >&2
exit 1
`))
	if err == nil || !strings.HasPrefix(err.Error(), "cua-driver does not start: cua-driver ended: xxx") || !strings.HasSuffix(err.Error(), "x no display") || len(err.Error()) > 2200 {
		t.Fatalf("failing: %v", err)
	}

	// One that is no program does not start.
	broken := filepath.Join(t.TempDir(), "cua-driver")
	if err := os.WriteFile(broken, []byte{0, 1, 2, 3}, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = launch(ctx, broken); err == nil {
		t.Fatal("started what is no program")
	}

	_, err = launch(ctx, filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "cua-driver is not installed") {
		t.Fatalf("missing: %v", err)
	}
}

// An error of a tool of the driver is the error of the call, and a driver
// that ends fails the calls that follow it.
func TestCallAndEnd(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "cua-driver", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "list_windows", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "bad params"}}}, nil
	})
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := connect(context.Background(), "x", clientTransport, func() { ss.Close() }, func() string { return "it broke" })
	if err != nil {
		t.Fatal(err)
	}
	defer d.end()
	if _, err := d.call(context.Background(), "list_windows", nil); err == nil || err.Error() != "cua-driver: bad params" {
		t.Fatalf("an error: %v", err)
	}
	ss.Close() // the driver ends
	waitFor(t, d.closed)
	if _, err := d.call(context.Background(), "list_windows", nil); err == nil {
		t.Fatal("a call after the end was answered")
	}
}

// The driver runs in the process ouro starts on Linux, and in its own app on
// every other system, which holds the permissions macOS grants.
func TestArguments(t *testing.T) {
	for goos, want := range map[string][]string{
		"linux":  {"mcp", "--direct"},
		"darwin": {"mcp"},
	} {
		if got := arguments(goos); !slices.Equal(got, want) {
			t.Errorf("arguments(%q) = %q, want %q", goos, got, want)
		}
	}
}

// The text of a result is that of its contents that are text and say
// something.
func TestResultText(t *testing.T) {
	r := &result{&mcpsdk.CallToolResult{Content: []mcpsdk.Content{
		&mcpsdk.TextContent{Text: "  "},
		&mcpsdk.ImageContent{Data: []byte("png")},
		&mcpsdk.TextContent{Text: " a "},
		&mcpsdk.TextContent{Text: "b"},
	}}}
	if got := r.text(); got != "a\nb" {
		t.Errorf("text %q", got)
	}
}

// A driver that never greets ends with what it started, as one that ouro
// ends does: the group goes, not only the program.
func TestFailedHandshakeKillsTheGroup(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	gone := make(chan error, 1)
	go func() {
		f, err := os.Open(fifo)
		if err == nil {
			_, err = f.Read(make([]byte, 1))
			f.Close()
		}
		gone <- err
	}()
	// The program starts a child that holds the FIFO open, closes the output
	// the SDK greets it on, and stays without answering.
	_, err := launch(context.Background(), script(t, "sh -c 'exec 3>"+fifo+"; sleep 60' >&- &\nexec >&-\nexec sleep 60\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "cua-driver does not start") {
		t.Fatalf("a driver that does not greet: %v", err)
	}
	select {
	case err := <-gone:
		if err == nil {
			t.Fatal("what it started said something")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("what the driver started is still running")
	}
}
