package computer

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
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

// launch starts the program, greets it and tells it to send no telemetry;
// it ends with its input closed, or is killed.
func TestLaunch(t *testing.T) {
	ctx := context.Background()
	d, err := launch(ctx, script(t, `read -r line
echo '{"jsonrpc":"2.0","id":1,"result":{}}'
read -r line
read -r line
printf '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"%s %s %s"}]}}\n' "$CUA_DRIVER_RS_TELEMETRY_ENABLED" "$CUA_TELEMETRY_ENABLED" "$CUA_DRIVER_RS_ENABLE_WAYLAND"
cat >/dev/null
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := d.call(ctx, "list_windows", nil)
	if err != nil || r.text() != "false false 1" {
		t.Fatalf("telemetry: %v, %v", r, err)
	}
	d.end()
	if !d.closed() {
		t.Fatal("ended, but not closed")
	}

	// One that does not end with its input is killed.
	old := closeTimeout
	closeTimeout = 50 * time.Millisecond
	defer func() { closeTimeout = old }()
	d, err = launch(ctx, script(t, `read -r line
echo '{"jsonrpc":"2.0","id":1,"result":{}}'
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

// What the driver sends that is no answer to a request is passed over, an
// error it answers with is the error of the request, and a driver that ends
// fails the requests that wait.
func TestProtocolEdges(t *testing.T) {
	toDriver, fromServer := io.Pipe()
	fromDriver, toServer := io.Pipe()
	d := connect("x", toDriver, toServer, func() { toServer.Close(); fromServer.Close() }, func() string { return "" })
	defer d.end()
	go func() {
		r := bufio.NewReader(fromDriver)
		r.ReadBytes('\n')
		io.WriteString(fromServer, "not JSON\n\n"+
			`{"jsonrpc":"2.0","method":"notifications/message","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":"x","result":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":99,"result":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad params"}}`+"\n")
		r.ReadBytes('\n')
		fromServer.Close() // ends while the second request waits
		io.Copy(io.Discard, r)
	}()
	ctx := context.Background()
	if err := d.request(ctx, "a", nil, nil); err == nil || err.Error() != "cua-driver: bad params" {
		t.Fatalf("an error: %v", err)
	}
	if err := d.request(ctx, "b", nil, nil); err == nil || err.Error() != "cua-driver ended" {
		t.Fatalf("ended while waiting: %v", err)
	}

	// A driver that takes no more input fails the request.
	in, _ := io.Pipe()
	_, out := io.Pipe()
	out.Close()
	d2 := connect("x", in, out, func() { in.Close() }, func() string { return "" })
	defer d2.end()
	if err := d2.request(ctx, "a", nil, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed input: %v", err)
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
