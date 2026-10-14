package browser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// pipe is a connection to a browser the test plays: it reads the commands
// from commands and writes what the browser says to say.
func pipe(t *testing.T) (c *conn, commands *bufio.Reader, say func(string)) {
	t.Helper()
	cr, cw := io.Pipe()
	rr, rw := io.Pipe()
	t.Cleanup(func() { rw.Close(); cr.Close() })
	return newConn(rr, cw), bufio.NewReader(cr), func(s string) { rw.Write([]byte(s + "\x00")) }
}

// The connection hands replies to their calls and events to the sessions
// listened to, and skips what it cannot read.
func TestConn(t *testing.T) {
	c, commands, say := pipe(t)
	ctx := context.Background()
	events := c.listen("S")
	done := make(chan error)
	var got struct {
		Product string `json:"product"`
	}
	go func() { done <- c.call(ctx, "S", "Browser.getVersion", map[string]int{"a": 1}, &got) }()
	sent, _ := commands.ReadString(0)
	if sent != `{"id":1,"method":"Browser.getVersion","params":{"a":1},"sessionId":"S"}`+"\x00" {
		t.Fatalf("sent %q", sent)
	}
	say("not json")
	say(`{"id":7,"result":{}}`)                                             // a call nobody waits for
	say(`{"method":"Page.loadEventFired","params":{},"sessionId":"other"}`) // not listened to
	for i := range 70 {                                                     // more than are kept
		say(fmt.Sprintf(`{"method":"Page.frameStartedLoading","params":{"n":%d},"sessionId":"S"}`, i))
	}
	say(`{"method":"Target.detachedFromTarget","params":{"sessionId":"S"}}`)
	say(`{"id":1,"result":{"product":"Chrome"}}`)
	if err := <-done; err != nil || got.Product != "Chrome" {
		t.Fatalf("got %v, %v", got, err)
	}
	if n := len(events); n != 64 {
		t.Fatalf("%d events kept", n)
	}
	if e := <-events; e.method != "Page.frameStartedLoading" || string(e.params) != `{"n":0}` {
		t.Fatalf("first event %v", e)
	}
	if !c.gone("S") || c.gone("T") {
		t.Fatal("the session is not gone")
	}
	c.forget("S")
	if c.gone("S") {
		t.Fatal("a session forgotten is still gone")
	}

	// An error, and a call without result.
	go func() { done <- c.call(ctx, "", "Target.closeTarget", nil, nil) }()
	commands.ReadString(0)
	say(`{"id":2,"error":{"code":-32000,"message":"No target with given id"}}`)
	var refused *cdpError
	if err := <-done; !errors.As(err, &refused) || err.Error() != "Target.closeTarget: No target with given id" {
		t.Fatalf("refused: %v", err)
	}
	go func() { done <- c.call(ctx, "", "Page.enable", nil, nil) }()
	commands.ReadString(0)
	say(`{"id":3,"result":{"whatever":1}}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// A call whose context ends before the reply.
	cut, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	go func() { done <- c.call(cut, "", "Page.enable", nil, nil) }()
	commands.ReadString(0)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ended: %v", err)
	}
	say(`{"id":4,"result":{}}`) // too late: nobody waits for it
}

// Of a session it does not listen to the connection knows no dialog, which
// never comes, and no console.
func TestConnUnknownSession(t *testing.T) {
	c, _, _ := pipe(t)
	d, next := c.dialog("S")
	if d != nil || next.done != nil {
		t.Fatalf("dialog %v, next %v", d, next)
	}
	if lines, dropped := c.console("S"); lines != nil || dropped != 0 {
		t.Fatalf("console %v, %d", lines, dropped)
	}
	c.answered("S")
}

// A browser that goes ends the calls that wait, and those after.
func TestConnClosed(t *testing.T) {
	cr, cw := io.Pipe()
	rr, rw := io.Pipe()
	c := newConn(rr, cw)
	done := make(chan error)
	go func() { done <- c.call(context.Background(), "", "Browser.getVersion", nil, nil) }()
	bufio.NewReader(cr).ReadString(0)
	if c.closed() {
		t.Fatal("closed before it is")
	}
	rw.Close()
	if err := <-done; !errors.Is(err, errClosed) {
		t.Fatalf("a call that waited: %v", err)
	}
	if !c.closed() {
		t.Fatal("not closed")
	}
	if err := c.call(context.Background(), "", "Browser.getVersion", nil, nil); !errors.Is(err, errClosed) {
		t.Fatalf("a call after: %v", err)
	}

	// A browser that no longer reads.
	c = newConn(strings.NewReader(""), failing{})
	<-c.done
	c = newConn(blocking{}, failing{})
	if err := c.call(context.Background(), "", "Browser.getVersion", nil, nil); !errors.Is(err, errClosed) {
		t.Fatalf("a call that cannot be written: %v", err)
	}
}

// failing is a pipe to a browser that went.
type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (failing) Close() error              { return nil }

// blocking is a browser that says nothing.
type blocking struct{}

func (blocking) Read([]byte) (int, error) { select {} }
