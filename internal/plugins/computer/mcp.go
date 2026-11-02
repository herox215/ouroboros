package computer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ouroboros/internal/procgroup"
)

// closeTimeout is about how long ending cua-driver may take, all in all;
// tests shorten it. Once its input is closed the SDK waits a third of it for
// it to end by itself, then a third more after SIGTERM, and SIGKILLs it after
// another third; the group it leads is killed then, which takes what it
// started along with it.
var closeTimeout = 3 * time.Second

// driver is cua-driver that runs, spoken to over MCP by the SDK of the
// protocol (github.com/modelcontextprotocol/go-sdk).
type driver struct {
	path    string // the program, to tell whether the settings name another
	session *mcpsdk.ClientSession
	stop    func()        // ends the program and waits for it
	gone    chan struct{} // closed once the driver ended

	mu  sync.Mutex // guards what follows
	err error      // why the driver ended, once it did
}

// result is what a tool of the driver returned.
type result struct{ *mcpsdk.CallToolResult }

// text is the text of what the tool returned: those of its contents that are
// text and say something, joined by a newline.
func (r *result) text() string {
	var parts []string
	for _, content := range r.Content {
		if c, ok := content.(*mcpsdk.TextContent); ok {
			if text := strings.TrimSpace(c.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// structured reads what the tool says besides its contents into v.
func (r *result) structured(v any) error {
	if r.StructuredContent == nil {
		return errors.New("nothing beyond its contents")
	}
	b, err := json.Marshal(r.StructuredContent) // values of JSON: cannot fail
	if err != nil {
		return err
	}
	// Numbers are read as they are written: window ids are uint64, which a
	// float64 of JSON rounds above 2^53.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

// launch starts the program path as the MCP server of cua-driver and greets
// it. It tells the driver to send no telemetry: what the agent does on the
// computer of the owner is nobody else's business.
func launch(ctx context.Context, path string) (*driver, error) {
	program, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("cua-driver is not installed (%v): install it, on Omarchy with pacman -S cua-driver-bin, or see https://github.com/trycua/cua", err)
	}
	// The driver gets a group of its own, so that everything it starts ends
	// with it, also what it launches as the agent uses it.
	life, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(life, program, arguments(runtime.GOOS)...)
	// Its backend for Wayland is still to be switched on: without it the
	// driver sees no window of a desktop like Hyprland.
	cmd.Env = append(os.Environ(), "CUA_DRIVER_RS_TELEMETRY_ENABLED=false", "CUA_TELEMETRY_ENABLED=false", "CUA_DRIVER_RS_ENABLE_WAYLAND=1")
	var stderr tail
	cmd.Stderr = &stderr
	procgroup.Detach(cmd)
	// Why it ended is the last of what it wrote, which is all there once the
	// command was waited for: reading its error output ends before that. The
	// SDK waits for the command when it closes the connection, so the session
	// of a driver ends, and launch returns, once all of its output was kept.
	why := func() string { return stderr.String() }
	ctx, deadline := context.WithTimeout(ctx, 20*time.Second)
	defer deadline()
	var d *driver
	d, err = connect(ctx, path, &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: closeTimeout / 3}, func() {
		// Its input closes, so that it ends; what it started goes with the
		// group, which closing its input alone leaves behind.
		d.session.Close()
		_ = procgroup.Kill(cmd) // the group, which closing its input leaves
		cancel()
	}, why)
	if err != nil {
		// What was started for it ends with it, group and all, and what it
		// wrote says why it did not start. It did start when the SDK got as
		// far as greeting it, which is the case where its own words are worth
		// more than the error of the SDK: the SDK closed the connection it
		// could not greet, and waited for the command there, so what it wrote
		// is complete. Start sets Process, and only Start, which has returned
		// by now, so no goroutine writes what this reads.
		_ = procgroup.Kill(cmd)
		cancel()
		if cmd.Process != nil {
			return nil, fmt.Errorf("cua-driver does not start: %s", ended(why))
		}
		return nil, fmt.Errorf("cua-driver does not start: %w", err)
	}
	return d, nil
}

// arguments are those cua-driver is started with on the system goos. On
// Linux --direct runs the driver in the process ouro starts, which ends with
// it, instead of in a daemon that outlives ouro. On macOS the driver runs in
// that daemon, the app CuaDriver, which holds the permissions for
// Accessibility and Screen Recording: run directly, macOS would grant them
// to the terminal ouro runs in, and so to every command run there.
func arguments(goos string) []string {
	if goos == "linux" {
		return []string{"mcp", "--direct"}
	}
	return []string{"mcp"}
}

// connect connects the driver at path over transport, which stop ends, and
// returns it once it greeted. why tells why it ended, once it did.
func connect(ctx context.Context, path string, transport mcpsdk.Transport, stop func(), why func() string) (*driver, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ouro", Version: "1"}, &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{}, // neither sampling nor elicitation nor roots
	})
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	d := &driver{path: path, session: session, stop: stop, gone: make(chan struct{})}
	go d.watch(why)
	return d, nil
}

// watch waits for the driver to end, and notes why, for the calls that
// follow.
func (d *driver) watch(why func() string) {
	defer close(d.gone)
	d.session.Wait()
	d.mu.Lock()
	d.err = errors.New(ended(why))
	d.mu.Unlock()
}

// call runs the tool name of the driver with args.
func (d *driver) call(ctx context.Context, name string, args map[string]any) (*result, error) {
	d.mu.Lock()
	ended := d.err
	d.mu.Unlock()
	if ended != nil {
		return nil, ended
	}
	answer, err := d.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// What the driver answered, or failed to answer, is the driver
		// speaking, and its texts stay recognisable wherever they are embedded.
		return nil, fmt.Errorf("cua-driver: %w", err)
	}
	r := &result{answer}
	if answer.IsError {
		return nil, fmt.Errorf("cua-driver: %s", cmp.Or(r.text(), name+" failed"))
	}
	return r, nil
}

// ended is why the driver ended, as a call reads it: that it did, and the
// last of what it wrote.
func ended(why func() string) string {
	if s := strings.TrimSpace(why()); s != "" {
		return "cua-driver ended: " + s
	}
	return "cua-driver ended"
}

// closed reports whether the driver ended.
func (d *driver) closed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err != nil
}

// end ends the driver, and returns once it is closed.
func (d *driver) end() {
	d.stop()
	<-d.gone
}

// tail keeps the last lines a program wrote, to say why it ended.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
