package computer

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// closeTimeout is how long cua-driver may take to end once its input is
// closed, before it is killed; tests shorten it.
var closeTimeout = 3 * time.Second

// driver is cua-driver that runs, spoken to over MCP: JSON-RPC, one message
// per line, on its standard input and output.
type driver struct {
	path string // the program, to tell whether the settings name another
	out  io.WriteCloser
	stop func() // ends the program and waits for it

	write sync.Mutex // guards out

	mu      sync.Mutex // guards what follows
	next    int64
	waiting map[int64]chan reply
	err     error // why the driver ended, once it did
}

// reply is the answer of the driver to a request.
type reply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// result is what a tool of the driver returns.
type result struct {
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
	IsError    bool            `json:"isError"`
}

// text is the text of what the tool returned.
func (r *result) text() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// launch starts the program path as the MCP server of cua-driver and greets
// it. It tells the driver to send no telemetry: what the agent does on the
// computer of the owner is nobody else's business.
func launch(ctx context.Context, path string) (*driver, error) {
	program, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("cua-driver is not installed (%v): install it, on Omarchy with pacman -S cua-driver-bin, or see https://github.com/trycua/cua", err)
	}
	cmd := exec.Command(program, arguments(runtime.GOOS)...)
	// Its backend for Wayland is still to be switched on: without it the
	// driver sees no window of a desktop like Hyprland.
	cmd.Env = append(os.Environ(), "CUA_DRIVER_RS_TELEMETRY_ENABLED=false", "CUA_TELEMETRY_ENABLED=false", "CUA_DRIVER_RS_ENABLE_WAYLAND=1")
	requests, in, _ := os.Pipe() // fails only without file descriptors, as the start would
	out, answers, _ := os.Pipe()
	var stderr tail
	cmd.Stdin, cmd.Stdout, cmd.Stderr = requests, answers, &stderr
	err = cmd.Start()
	// The driver has its ends now, which close when it ends.
	requests.Close()
	answers.Close()
	if err != nil {
		in.Close()
		out.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	d := connect(path, out, in, func() {
		in.Close()
		select {
		case <-done:
		case <-time.After(closeTimeout):
			cmd.Process.Kill()
			<-done
		}
	}, stderr.String)
	if err := d.greet(ctx); err != nil {
		d.end()
		return nil, err
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

// connect returns the driver that answers on in and is asked on out, which
// stop ends. what tells why it ended, once it did.
func connect(path string, in io.Reader, out io.WriteCloser, stop func(), what func() string) *driver {
	d := &driver{path: path, out: out, stop: stop, waiting: map[int64]chan reply{}}
	go d.read(in, what)
	return d
}

// greet starts the MCP session.
func (d *driver) greet(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var hello struct {
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	err := d.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ouro", "version": "1"},
	}, &hello)
	if err != nil {
		return fmt.Errorf("cua-driver does not start: %w", err)
	}
	return d.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}

// call runs the tool name of the driver with args.
func (d *driver) call(ctx context.Context, name string, args map[string]any) (*result, error) {
	var r result
	if err := d.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &r); err != nil {
		return nil, err
	}
	if r.IsError {
		return nil, errors.New(cmp.Or(r.text(), name+" failed"))
	}
	return &r, nil
}

// request sends method with params and decodes the result into v.
func (d *driver) request(ctx context.Context, method string, params, v any) error {
	d.mu.Lock()
	if d.err != nil {
		d.mu.Unlock()
		return d.err
	}
	d.next++
	id := d.next
	ch := make(chan reply, 1)
	d.waiting[id] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.waiting, id)
		d.mu.Unlock()
	}()
	if err := d.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r, ok := <-ch:
		if !ok {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.err
		}
		if r.Error != nil {
			return fmt.Errorf("cua-driver: %s", r.Error.Message)
		}
		return json.Unmarshal(r.Result, v)
	}
}

// send writes one message to the driver.
func (d *driver) send(message any) error {
	b, _ := json.Marshal(message) // maps of plain values: cannot fail
	d.write.Lock()
	defer d.write.Unlock()
	_, err := d.out.Write(append(b, '\n'))
	return err
}

// read hands the answers of the driver to the requests that wait for them,
// until it ends.
func (d *driver) read(in io.Reader, what func() string) {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			d.handle(line)
		}
		if err != nil {
			break
		}
	}
	why := "cua-driver ended"
	if s := strings.TrimSpace(what()); s != "" {
		why += ": " + s
	}
	d.mu.Lock()
	d.err = errors.New(why)
	for id, ch := range d.waiting {
		close(ch)
		delete(d.waiting, id)
	}
	d.mu.Unlock()
}

// handle takes one message of the driver.
func (d *driver) handle(line []byte) {
	var m struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
		reply
	}
	if json.Unmarshal(line, &m) != nil || m.ID == nil {
		return // a notification, or no message
	}
	if m.Method != "" {
		// A request of the driver: ouro offers it nothing.
		d.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported"}})
		return
	}
	var id int64
	if json.Unmarshal(*m.ID, &id) != nil {
		return
	}
	d.mu.Lock()
	ch := d.waiting[id]
	d.mu.Unlock()
	if ch != nil {
		ch <- m.reply
	}
}

// closed reports whether the driver ended.
func (d *driver) closed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err != nil
}

// end ends the driver.
func (d *driver) end() { d.stop() }

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
