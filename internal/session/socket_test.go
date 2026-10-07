package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
)

// fakeNode is a node in a directory of its own as far as its front ends can
// tell: it holds its PID file and serves handler on its socket.
type fakeNode struct {
	dir  home.Dir
	stop func() // the node is gone
}

// serve runs a node that serves handler, until the test ends.
func serve(t *testing.T, handler http.Handler) *fakeNode {
	t.Helper()
	dir := home.Dir(t.TempDir())
	lease, err := daemon.Hold(dir.Service().PIDFile, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := dir.Socket()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	var once sync.Once
	n := &fakeNode{dir: dir, stop: func() {
		once.Do(func() {
			srv.Close()
			lease.Release()
		})
	}}
	t.Cleanup(n.stop)
	return n
}

// The front ends do over the socket what they do in the process of the node:
// the same events, the same list, the same errors.
func TestClient(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{size: 7, run: func(ctx context.Context, text string, observe func(assistant.Step), say func(string)) (string, error) {
		if text == "wait" {
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		say("")
		observe(assistant.Step{Tool: "read", Done: true, Err: errors.New("gone")})
		return "pong", nil
	}}
	s := service(t, a)
	n := serve(t, Handler(s))
	c := NewClient(n.dir)
	ctx := context.Background()

	if started, err := c.Open(ctx); started || err != nil {
		t.Fatalf("open a node that runs: %v, %v", started, err)
	}
	id, err := c.New(ctx)
	if err != nil || len(id) != 36 {
		t.Fatalf("new: %q, %v", id, err)
	}
	past, live, err := c.Watch(ctx, id)
	if err != nil || len(past) != 0 {
		t.Fatalf("watch: %+v, %v", past, err)
	}
	if err := c.Ask(ctx, id, "ping"); err != nil {
		t.Fatal(err)
	}
	got := until(t, live, Answer)
	if kinds(got) != "message say step answer" || got[3].Text != "pong" || got[2].AsStep().Err.Error() != "gone" || got[3].Context != 7 {
		t.Fatalf("events %+v", got)
	}
	if past, _, _ := c.Watch(ctx, id); kinds(past) != "message say step answer" || !past[3].At.Equal(got[3].At) {
		t.Fatalf("past %+v", past)
	}
	if list, err := c.List(ctx); err != nil || len(list) != 1 || list[0].ID != id || list[0].Title != "ping" {
		t.Fatalf("list %+v, %v", list, err)
	}

	// Errors the front ends tell apart come back as they were.
	c.Ask(ctx, id, "wait")
	if err := c.Ask(ctx, id, "more"); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if err := c.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	until(t, live, Canceled)
	close(gate)
	if _, err := c.Contents(ctx, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("contents of no session: %v", err)
	}
	if _, _, err := c.Watch(ctx, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("watch no session: %v", err)
	}
	if err := c.Ask(ctx, id, ""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}
	a.ready = config.ErrIncomplete
	if err := c.Ask(ctx, id, "hi"); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("not set up: %v", err)
	}
	a.ready = errors.New("tools.h2a: settings of the tool read: no")
	if err := c.Ask(ctx, id, "hi"); err == nil || err.Error() != a.ready.Error() {
		t.Fatalf("not ready: %v", err)
	}

	// The context, and what of it there is with an error.
	a.contents.System = "You are alpha."
	if lines, err := c.Contents(ctx, id); err != nil || len(lines) != 1 || lines[0] != (Line{"system", "You are alpha."}) {
		t.Fatalf("contents %+v, %v", lines, err)
	}
	a.failed = errors.New("not set up")
	if lines, err := c.Contents(ctx, id); err == nil || err.Error() != "not set up" || len(lines) != 1 {
		t.Fatalf("contents %+v, %v", lines, err)
	}
}

// A front end that started the node lets it stop once no answer is on its
// way, and waits until it stopped.
func TestClientRelease(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		<-gate
		return "pong", nil
	}}
	s := service(t, a)
	n := serve(t, Handler(s))
	s.Attach(func() { go n.stop() }, nil)
	c := NewClient(n.dir)
	ctx := context.Background()
	_, live, _ := c.Watch(ctx, "s1")
	c.Ask(ctx, "s1", "hi")
	if err := c.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, running := c.local.Running(); !running {
		t.Fatal("the answer keeps the node running")
	}
	close(gate)
	until(t, live, Answer)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, running := c.local.Running(); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the node should stop")
		}
	}
	// A node that does not run is stopped already.
	if err := c.Release(ctx); err != nil {
		t.Fatal(err)
	}

	// One that is released and stops now is waited for, but not forever.
	idle := service(t, &fakeAgent{})
	slow := serve(t, Handler(idle))
	idle.Attach(func() { go func() { time.Sleep(100 * time.Millisecond); slow.stop() }() }, nil)
	waiting := NewClient(slow.dir)
	if err := waiting.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, running := waiting.local.Running(); running {
		t.Fatal("the node should have stopped")
	}
	stuck := service(t, &fakeAgent{})
	stuck.Attach(func() {}, nil)
	impatient := NewClient(serve(t, Handler(stuck)).dir)
	impatient.Patience = 100 * time.Millisecond
	if err := impatient.Release(ctx); err == nil || err.Error() != "the node did not stop in time" {
		t.Fatalf("a node that does not stop: %v", err)
	}
}

// Without a node there are no sessions, and a node that does not listen on
// its socket is waited for a while.
func TestClientWithoutNode(t *testing.T) {
	dir := home.Dir(t.TempDir())
	c := NewClient(dir)
	ctx := context.Background()
	if err := c.Ask(ctx, "s1", "hi"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("ask: %v", err)
	}
	if _, _, err := c.Watch(ctx, "s1"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("watch: %v", err)
	}
	if _, err := c.List(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("list: %v", err)
	}
	if _, err := c.Contents(ctx, "s1"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("contents: %v", err)
	}
	if _, err := c.New(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("new: %v", err)
	}
	if err := c.Cancel(ctx, "s1"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel: %v", err)
	}
	if started, err := c.Open(ctx); started || !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("open a node that is not set up: %v, %v", started, err)
	}

	lease, err := daemon.Hold(dir.Service().PIDFile, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	c.Listening = 100 * time.Millisecond
	if err := c.Ask(ctx, "s1", "hi"); err == nil || !strings.Contains(err.Error(), "the node does not answer on "+dir.Socket()+", is it of an older build?") {
		t.Fatalf("a node that does not listen: %v", err)
	}
	c.Listening = time.Minute
	gone, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := c.Ask(gone, "s1", "hi"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("given up: %v", err)
	}
}

// What the node answers that is no answer of a Service is said so.
func TestClientOfAStrangeNode(t *testing.T) {
	n := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/watch":
			io.WriteString(w, "nonsense\n")
		case "/list":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "oops")
		default:
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"no such thing","code":"unknown"}`)
		}
	}))
	c := NewClient(n.dir)
	ctx := context.Background()
	if _, _, err := c.Watch(ctx, "s1"); err == nil || !strings.Contains(err.Error(), "the node did not say what happened") {
		t.Fatalf("watch: %v", err)
	}
	if _, err := c.List(ctx); err == nil || err.Error() != "the node answered 500 Internal Server Error" {
		t.Fatalf("list: %v", err)
	}
	if err := c.Ask(ctx, "s1", "hi"); err == nil || err.Error() != "no such thing" {
		t.Fatalf("ask: %v", err)
	}
}

// A request the Service cannot read is refused as that.
func TestHandlerRefusesWhatItCannotRead(t *testing.T) {
	h := Handler(service(t, &fakeAgent{}))
	for _, path := range []string{"/ask", "/cancel"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{")))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"error":"the request cannot be read: `) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

// What a front end watches ends with its context, and when the node stops;
// what it did not take by then is dropped.
func TestWatchEnds(t *testing.T) {
	s := service(t, &fakeAgent{})
	n := serve(t, Handler(s))
	c := NewClient(n.dir)
	ctx, cancel := context.WithCancel(context.Background())
	_, live, err := c.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	c.Ask(context.Background(), "s1", "hi")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if list, _ := s.List(context.Background()); len(list) == 1 && !list[0].Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer should be given")
		}
	}
	time.Sleep(50 * time.Millisecond) // the first event is read and waits to be taken
	cancel()
	for range live {
	}

	_, live, _ = c.Watch(context.Background(), "s1")
	s.Close()
	for range live {
	}
}

// A PID file can become visible before the socket is listening. A client
// with its default startup allowance must survive that normal startup gap.
func TestClientWaitsForListeningNode(t *testing.T) {
	dir := home.Dir(t.TempDir())
	lease, err := daemon.Hold(dir.Service().PIDFile, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	path := dir.Socket()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	c := NewClient(dir)
	transport := c.http.Transport.(*http.Transport)
	originalDial := transport.DialContext
	started := make(chan struct{})
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		close(started)
		return originalDial(ctx, network, address)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		list, err := c.List(ctx)
		if err == nil && len(list) != 0 {
			err = fmt.Errorf("unexpected sessions: %+v", list)
		}
		done <- err
	}()
	<-started
	// This gap exceeds a retry interval and stays well inside startup patience.
	time.Sleep(125 * time.Millisecond)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(service(t, &fakeAgent{}))}
	defer srv.Close()
	go srv.Serve(ln)
	if err := <-done; err != nil {
		t.Fatalf("client should wait for the node's socket: %v", err)
	}
}

// Neither an empty error message nor a partially decoded response is a
// trustworthy service error. Report the HTTP status instead of stale data.
func TestClientRejectsMalformedErrorResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"error":""}`, `{"error":"partially decoded","code":3}`} {
		t.Run(body, func(t *testing.T) {
			n := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, body)
			}))
			err := NewClient(n.dir).Ask(t.Context(), "s1", "hi")
			if err == nil || err.Error() != "the node answered 409 Conflict" {
				t.Fatalf("malformed response %s: %v", body, err)
			}
		})
	}
}
