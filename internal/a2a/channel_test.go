package a2a

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/contacts"
	"ouroboros/internal/identity"
)

// patience is how long a test waits for what should happen at once.
const patience = 10 * time.Second

// eventually waits until ok reports true.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(patience); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not in time", what)
		}
	}
}

// receive is ch.Receive for a test that must not wait forever.
func receive(t *testing.T, ch *Channel) ([]byte, error) {
	t.Helper()
	type result struct {
		msg []byte
		err error
	}
	got := make(chan result, 1)
	go func() {
		msg, err := ch.Receive()
		got <- result{msg, err}
	}()
	select {
	case r := <-got:
		return r.msg, r.err
	case <-time.After(patience):
		t.Fatal("nothing was received in time")
		return nil, nil
	}
}

// expect receives the message want from ch.
func expect(t *testing.T, ch *Channel, want string) {
	t.Helper()
	if msg, err := receive(t, ch); err != nil || string(msg) != want {
		t.Fatalf("received %q, %v, want %q", msg, err, want)
	}
}

// closedWith receives from ch, which is expected to be closed because of want.
func closedWith(t *testing.T, ch *Channel, want error) {
	t.Helper()
	if msg, err := receive(t, ch); !errors.Is(err, want) || msg != nil {
		t.Fatalf("received %q, %v, want %v", msg, err, want)
	}
}

// link returns the two ends of a connection on this host.
func link(t *testing.T) (dialed, accepted net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if dialed, err = net.Dial("tcp", ln.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if accepted, err = ln.Accept(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dialed.Close()
		accepted.Close()
	})
	return dialed, accepted
}

// pair returns the two ends of a channel between the nodes "a" and "b" that
// is idle after idle and waits wait for the peer when it is closed.
func pair(t *testing.T, idle, wait time.Duration) (a, b *Channel) {
	t.Helper()
	dialed, accepted := link(t)
	a = newChannel(dialed, dialed, "b", idle, wait)
	b = newChannel(accepted, accepted, "a", idle, wait)
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})
	return a, b
}

// oneEnd returns a channel whose other end is the connection peer: for what a
// node does that sends something else than a channel would.
func oneEnd(t *testing.T) (ch *Channel, peer net.Conn) {
	t.Helper()
	dialed, accepted := link(t)
	ch = newChannel(dialed, dialed, "peer", time.Hour, 20*time.Millisecond)
	t.Cleanup(func() { ch.Close() })
	return ch, accepted
}

// Both ends send and receive whole messages, in the order they were sent.
func TestChannelCarriesMessagesBothWays(t *testing.T) {
	a, b := pair(t, time.Hour, time.Second)
	if a.Peer() != "b" || b.Peer() != "a" {
		t.Fatalf("peers %q and %q", a.Peer(), b.Peer())
	}
	for _, msg := range []string{"one", "", "three"} {
		if err := a.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		expect(t, b, msg)
		if err := b.Send([]byte("got " + msg)); err != nil {
			t.Fatal(err)
		}
		expect(t, a, "got "+msg)
	}

	// A message is as long as MaxChannelMessage and no longer.
	large := bytes.Repeat([]byte{7}, MaxChannelMessage)
	sent := make(chan error, 1)
	go func() { sent <- a.Send(large) }()
	if msg, err := receive(t, b); err != nil || !bytes.Equal(msg, large) {
		t.Fatalf("the largest message: %d bytes, %v", len(msg), err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := a.Send(append(large, 7)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a message that is too large: %v", err)
	}
	// Which did not harm the channel.
	if err := a.Send([]byte("still open")); err != nil {
		t.Fatal(err)
	}
	expect(t, b, "still open")
}

// A channel that is closed tells both ends why. What was sent before it was
// closed still arrives.
func TestChannelClose(t *testing.T) {
	a, b := pair(t, time.Hour, time.Second)
	for _, msg := range []string{"one", "two"} {
		if err := a.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	expect(t, b, "one")
	expect(t, b, "two")
	closedWith(t, b, io.EOF)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := b.Send([]byte("late")); err != io.EOF {
		t.Fatalf("sending to a peer that closed: %v", err)
	}
	closedWith(t, a, ErrChannelClosed)
	if err := a.Send([]byte("late")); err != ErrChannelClosed {
		t.Fatalf("sending on a closed channel: %v", err)
	}
	for name, end := range map[string]struct {
		ch   *Channel
		want error
	}{"the end that closed": {a, ErrChannelClosed}, "the other end": {b, io.EOF}} {
		select {
		case <-end.ch.Context().Done():
		case <-time.After(patience):
			t.Fatalf("%s: the context is not done", name)
		}
		if cause := context.Cause(end.ch.Context()); cause != end.want {
			t.Errorf("%s: closed because of %v, want %v", name, cause, end.want)
		}
	}
	// Closing again changes nothing, with or without a reason.
	if err := errors.Join(a.Close(), b.Close(), a.CloseWith("again")); err != nil {
		t.Fatal(err)
	}
	closedWith(t, a, ErrChannelClosed)
	// What is to happen when a channel closes happens at once for a closed one.
	called := false
	a.onClose(func() { called = true })
	if !called {
		t.Fatal("a closed channel should release at once")
	}
}

// A reason given for closing reaches the peer, cut to size.
func TestChannelCloseWithReason(t *testing.T) {
	a, b := pair(t, time.Hour, time.Second)
	if err := a.CloseWith("the disk is full"); err != nil {
		t.Fatal(err)
	}
	_, err := receive(t, b)
	var closed *CloseError
	if !errors.As(err, &closed) || closed.Reason != "the disk is full" || !strings.Contains(err.Error(), "the disk is full") {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, &CloseError{"the disk is full"}) || errors.Is(err, ErrIdle) || errors.Is(err, io.EOF) {
		t.Fatalf("%v should be the reason that was given and no other", err)
	}
	closedWith(t, a, closed)

	// A reason is no place for a message: 256 bytes of it arrive, and no
	// half characters.
	a, b = pair(t, time.Hour, time.Second)
	a.CloseWith("x" + strings.Repeat("ä", maxReason))
	if _, err := receive(t, b); !errors.As(err, &closed) || closed.Reason != "x"+strings.Repeat("ä", maxReason/2-1) {
		t.Fatalf("a long reason: %v", err)
	}
	// And a peer that sends more gets no further.
	c, peer := oneEnd(t)
	writeFrame(peer, frameClose, []byte(strings.Repeat("x", maxReason+1)))
	if _, err := receive(t, c); !errors.As(err, &closed) || closed.Reason != strings.Repeat("x", maxReason) {
		t.Fatalf("a long reason of the peer: %v", err)
	}

	// Without a reason it is closed like any other.
	a, b = pair(t, time.Hour, time.Second)
	a.CloseWith("")
	closedWith(t, b, io.EOF)
	closedWith(t, a, ErrChannelClosed)

	// Both ends may close at once: neither waits for the other.
	a, b = pair(t, time.Hour, patience)
	var wg sync.WaitGroup
	start := time.Now()
	wg.Go(func() { a.CloseWith("a is done") })
	wg.Go(func() { b.CloseWith("b is done") })
	wg.Wait()
	if time.Since(start) >= patience {
		t.Fatal("two ends that close at once waited for each other")
	}
	// Who closed first says why, on its own end at least.
	for _, end := range []*Channel{a, b} {
		if _, err := receive(t, end); !errors.Is(err, &CloseError{"a is done"}) && !errors.Is(err, &CloseError{"b is done"}) {
			t.Fatalf("got %v", err)
		}
	}
}

// A channel nothing is sent over is closed, on both ends; one that is used
// stays open.
func TestChannelIdle(t *testing.T) {
	a, b := pair(t, 30*time.Millisecond, time.Second)
	closedWith(t, a, ErrIdle)
	closedWith(t, b, ErrIdle)
	if err := a.Send(nil); !errors.Is(err, ErrIdle) {
		t.Fatalf("sending on an idle channel: %v", err)
	}

	// Messages in one direction keep both ends from being idle.
	const idle = 500 * time.Millisecond
	a, b = pair(t, idle, time.Second)
	start := time.Now()
	for time.Since(start) < idle+idle/4 {
		if err := a.Send([]byte("tick")); err != nil {
			t.Fatalf("after %v: %v", time.Since(start), err)
		}
		expect(t, b, "tick")
		time.Sleep(idle / 10)
	}
	closedWith(t, a, ErrIdle)
	closedWith(t, b, ErrIdle)
	if time.Since(start) < 2*idle {
		t.Fatalf("closed %v after it was opened and used", time.Since(start))
	}
}

// What is no channel on the other side breaks the channel: a connection that
// ends without a word, and anything that is not a frame.
func TestChannelBreaks(t *testing.T) {
	header := func(kind byte, size uint32) []byte {
		return binary.BigEndian.AppendUint32([]byte{kind}, size)
	}
	for name, tt := range map[string]struct {
		sends []byte
		want  error
	}{
		"a connection that ends":            {nil, io.ErrUnexpectedEOF},
		"half a header":                     {[]byte{frameMessage, 0}, io.ErrUnexpectedEOF},
		"half a message":                    {append(header(frameMessage, 10), "half"...), io.ErrUnexpectedEOF},
		"an unknown kind of frame":          {header(2, 0), errFrame},
		"a message that is too large":       {header(frameMessage, MaxChannelMessage+1), errFrame},
		"something that is no frame at all": {[]byte("GET / HTTP/1.1\r\n\r\n"), errFrame},
	} {
		ch, peer := oneEnd(t)
		peer.Write(tt.sends)
		peer.Close()
		if _, err := receive(t, ch); !errors.Is(err, tt.want) {
			t.Errorf("%s: got %v, want %v", name, err, tt.want)
		}
		if err := ch.Send([]byte("hello?")); !errors.Is(err, tt.want) {
			t.Errorf("%s: sending afterwards: %v, want %v", name, err, tt.want)
		}
		if cause := context.Cause(ch.Context()); !errors.Is(cause, tt.want) {
			t.Errorf("%s: the context is done because of %v", name, cause)
		}
	}
}

// deaf is a connection nothing can be written to.
type deaf struct {
	net.Conn
	err error
}

func (d deaf) Write([]byte) (int, error) { return 0, d.err }

// stalled is a connection whose peer takes nothing: a write waits until the
// time it was given is over, and leaves half of what it was to write.
type stalled struct {
	net.Conn
	writing  chan struct{}  // gets a value when a write begins
	deadline chan time.Time // the time the writes were given
}

func (s stalled) SetWriteDeadline(t time.Time) error {
	s.deadline <- t
	return nil
}

func (s stalled) Write(p []byte) (int, error) {
	s.writing <- struct{}{}
	time.Sleep(time.Until(<-s.deadline))
	return len(p) / 2, os.ErrDeadlineExceeded
}

// What ends a channel cannot follow half a message: a Send that the peer does
// not take gets closeWait to finish, and if it does not, the connection is
// cut without a word.
func TestChannelClosedDuringASend(t *testing.T) {
	conn, _ := link(t)
	stuck := stalled{conn, make(chan struct{}, 1), make(chan time.Time, 1)}
	ch := newChannel(stuck, conn, "peer", time.Hour, 20*time.Millisecond)
	sent := make(chan error, 1)
	go func() { sent <- ch.Send([]byte("is anybody there?")) }()
	<-stuck.writing

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		ch.Close()
	}()
	select {
	case err := <-sent:
		if err != ErrChannelClosed {
			t.Fatalf("the Send that was stuck: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("closing did not end the Send that was stuck")
	}
	select {
	case <-closed:
	case <-time.After(patience):
		t.Fatal("the channel was not closed")
	}
	select {
	case <-stuck.writing:
		t.Fatal("something was written after half a message")
	default:
	}
}

// A message that cannot be sent closes the channel, and a channel whose peer
// cannot be told is closed all the same.
func TestChannelThatCannotSend(t *testing.T) {
	broken := errors.New("broken pipe")
	conn, _ := link(t)
	ch := newChannel(deaf{conn, broken}, conn, "peer", time.Hour, patience)
	if err := ch.Send([]byte("hi")); err != broken {
		t.Fatalf("got %v", err)
	}
	closedWith(t, ch, broken)
	if err := ch.Send([]byte("hi")); err != broken {
		t.Fatalf("sending again: %v", err)
	}

	// Closing does not wait for a peer that was never told.
	conn, _ = link(t)
	ch = newChannel(deaf{conn, broken}, conn, "peer", time.Hour, patience)
	start := time.Now()
	ch.Close()
	if time.Since(start) >= patience {
		t.Fatal("waited for a peer that was not told")
	}
	closedWith(t, ch, ErrChannelClosed)
}

// Send waits for a peer that does not receive, so that nothing piles up, and
// a channel with a Send that is stuck can still be closed.
func TestChannelSendWaitsForThePeer(t *testing.T) {
	a, b := pair(t, time.Hour, 20*time.Millisecond)
	piece := make([]byte, MaxChannelMessage)
	var sent atomic.Int32
	failed := make(chan error, 1)
	go func() {
		for {
			if err := a.Send(piece); err != nil {
				failed <- err
				return
			}
			sent.Add(1)
		}
	}()
	// Nobody receives: after what fits into the connection, Send waits.
	stuck := func() bool {
		n := sent.Load()
		time.Sleep(50 * time.Millisecond)
		return n > 0 && n == sent.Load()
	}
	eventually(t, "Send should wait for the peer", stuck)
	if n := sent.Load(); n > 64 {
		t.Fatalf("%d MiB were sent to a peer that does not receive", n)
	}
	// The peer gets what was sent as soon as it receives.
	if msg, err := receive(t, b); err != nil || len(msg) != len(piece) {
		t.Fatalf("got %d bytes, %v", len(msg), err)
	}
	eventually(t, "Send should wait again", stuck)

	a.Close()
	select {
	case err := <-failed:
		if err != ErrChannelClosed {
			t.Fatalf("the Send that was stuck: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("closing did not end the Send that was stuck")
	}
	// The peer gets the messages that got through. If the last one did not,
	// it finds the channel cut off.
	for {
		msg, err := receive(t, b)
		if err == nil && len(msg) == len(piece) {
			continue
		}
		if err != io.ErrUnexpectedEOF && err != io.EOF {
			t.Fatalf("got %d bytes, %v", len(msg), err)
		}
		break
	}
}

// A peer that does not take what was sent is not waited for longer than
// closeWait; what it was sent is still there for it.
func TestChannelCloseDoesNotWaitForever(t *testing.T) {
	a, b := pair(t, time.Hour, 200*time.Millisecond)
	for _, msg := range []string{"one", "two"} {
		if err := a.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	a.Close() // nobody receives on b
	expect(t, b, "one")
	expect(t, b, "two")
	closedWith(t, b, io.EOF)
}

// talk is a channel handler that greets whoever opens it and then answers
// every message in capitals, until the channel is closed. It reports why
// each of its channels ended.
type talk struct {
	ran   atomic.Int32
	ended chan error
}

func newTalk() *talk { return &talk{ended: make(chan error, 64)} }

func (k *talk) handler(name string) ChannelHandler {
	return ChannelHandler{Name: name, Handle: func(ctx context.Context, ch *Channel) {
		k.ran.Add(1)
		ch.Send([]byte("hello " + ch.Peer()))
		for {
			msg, err := ch.Receive()
			if err != nil {
				<-ctx.Done() // done with the channel, whoever closed it
				k.ended <- err
				return
			}
			ch.Send(bytes.ToUpper(msg))
		}
	}}
}

// why is why a channel of the handler ended.
func (k *talk) why(t *testing.T) error {
	t.Helper()
	select {
	case err := <-k.ended:
		return err
	case <-time.After(patience):
		t.Fatal("the handler did not end")
		return nil
	}
}

// connected returns the nodes alice and bob, of which bob has the channel
// "talk" and is connected to alice.
func connected(t *testing.T) (alice, bob *node, onBob *talk) {
	t.Helper()
	alice, bob, onBob = newNode(t, "alice"), newNode(t, "bob"), newTalk()
	bob.channels = []ChannelHandler{onBob.handler("talk")}
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")
	return alice, bob, onBob
}

// open opens the channel name of to as from.
func open(t *testing.T, from, to *node, name string) *Channel {
	t.Helper()
	ch, err := from.client().Open(context.Background(), to.srv.URL, to.id.Public(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	return ch
}

// A peer opens a channel a plugin of the other node added, and both ends talk
// until one of them closes it: here the one that opened it.
func TestChannelBetweenNodes(t *testing.T) {
	alice, bob, onBob := connected(t)
	asked := 0
	bob.respond = func(context.Context, string, string) (string, error) {
		asked++
		return "", nil
	}
	ch := open(t, alice, bob, "talk")
	// Each end knows the other by its key: bob from the signature of the
	// request, alice from the key the connection is pinned to.
	expect(t, ch, "hello "+alice.id.Public())
	if ch.Peer() != bob.id.Public() {
		t.Fatalf("the peer of alice is %q", ch.Peer())
	}
	for _, msg := range []string{"one", "two"} {
		if err := ch.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		expect(t, ch, strings.ToUpper(msg))
	}
	ch.Close()
	if err := onBob.why(t); err != io.EOF {
		t.Fatalf("the handler ended with %v", err)
	}
	if asked != 0 {
		t.Fatal("a channel is not a message to the agent")
	}
}

// The node that was asked for the channel closes it as well: when the
// handler returns, or with a reason.
func TestChannelClosedByTheHandler(t *testing.T) {
	alice, bob, _ := connected(t)
	bob.channels = []ChannelHandler{
		{Name: "once", Handle: func(_ context.Context, ch *Channel) {
			msg, _ := ch.Receive()
			ch.Send(append([]byte("bye "), msg...))
		}},
		{Name: "full", Handle: func(_ context.Context, ch *Channel) { ch.CloseWith("no room") }},
	}
	ch := open(t, alice, bob, "once")
	if err := ch.Send([]byte("alice")); err != nil {
		t.Fatal(err)
	}
	expect(t, ch, "bye alice")
	closedWith(t, ch, io.EOF)

	closedWith(t, open(t, alice, bob, "full"), &CloseError{"no room"})
}

// What does not fit into a request goes over a channel in pieces.
func TestChannelCarriesMoreThanARequest(t *testing.T) {
	alice, bob, _ := connected(t)
	bob.channels = []ChannelHandler{{Name: "sum", Handle: func(_ context.Context, ch *Channel) {
		sum := sha256.New()
		for {
			piece, err := ch.Receive()
			if err != nil {
				return
			}
			if len(piece) == 0 {
				ch.Send(sum.Sum(nil))
				return
			}
			sum.Write(piece)
		}
	}}}
	ch := open(t, alice, bob, "sum")
	file := make([]byte, 5*maxBodyBytes+12345)
	for i := range file {
		file[i] = byte(i * 31)
	}
	for rest := file; len(rest) > 0; {
		n := min(len(rest), 300_000)
		if err := ch.Send(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	if err := ch.Send(nil); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(file)
	if got, err := receive(t, ch); err != nil || !bytes.Equal(got, want[:]) {
		t.Fatalf("got %x, %v", got, err)
	}
}

// openBody is the request that opens the channel name.
func openBody(name string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"channel/open","params":{"name":"` + name + `"}}`
}

// A channel is behind the same checks as a method: the opening request has
// to be signed by the caller, for this node and this channel, fresh and new,
// and the caller has to be a peer the owner is connected to. Whoever fails
// one of them is answered like any caller that is refused, and the handler
// never runs.
func TestChannelIsBehindTheSameChecks(t *testing.T) {
	alice, bob, onBob := connected(t)
	carol := newNode(t, "carol")
	other := newTalk()
	bob.channels = append(bob.channels, other.handler("other"), other.handler(MethodSend))

	// send sends the request to open "talk" and returns the error bob
	// answers with, nil if the channel was opened.
	send := func(body string, change func(*http.Request)) *Error {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, bob.srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", ChannelProtocol)
		change(r)
		r.Body = io.NopCloser(strings.NewReader(body))
		resp, err := unpinned.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusSwitchingProtocols {
			if got := resp.Header.Get("Upgrade"); got != ChannelProtocol {
				t.Fatalf("upgraded to %q", got)
			}
			return nil
		}
		var res response
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.Error == nil {
			t.Fatalf("HTTP %d with %+v, %v", resp.StatusCode, res, err)
		}
		return res.Error
	}
	body := openBody("talk")
	refused := func(what string, err *Error, code int, message string) {
		t.Helper()
		if err == nil || err.Code != code || err.Message != message {
			t.Errorf("%s: got %+v, want %d %q", what, err, code, message)
		}
	}

	var recorded http.Header
	if err := send(body, func(r *http.Request) {
		sign(r, []byte(body), alice.id, 0, bob.id.Public(), MethodOpen)
		recorded = r.Header.Clone()
	}); err != nil {
		t.Fatalf("a connected peer should get the channel: %v", err)
	}
	// The connection was dropped without a word, which the handler notices.
	if err := onBob.why(t); err != io.ErrUnexpectedEOF {
		t.Fatalf("the handler ended with %v", err)
	}

	refused("no signature", send(body, func(*http.Request) {}), CodeUnauthorized, "missing signature")
	refused("replay", send(body, func(r *http.Request) { r.Header = recorded.Clone() }), CodeUnauthorized, "replayed request")
	refused("forged signature", send(body, func(r *http.Request) {
		sign(r, []byte(body), alice.id, 0, bob.id.Public(), MethodOpen)
		r.Header.Set(HeaderSig, base64.StdEncoding.EncodeToString(make([]byte, 64)))
	}), CodeUnauthorized, "bad signature")
	// A stranger who claims the key of the connected peer.
	refused("claimed key", send(body, func(r *http.Request) {
		sign(r, []byte(body), identity.Generate(), 0, bob.id.Public(), MethodOpen)
		r.Header.Set(HeaderPublic, alice.id.Public())
	}), CodeUnauthorized, "bad signature")
	// What alice signed for carol is nothing bob opens.
	refused("signed for another node", send(body, func(r *http.Request) {
		sign(r, []byte(body), alice.id, 0, carol.id.Public(), MethodOpen)
	}), CodeUnauthorized, "request is for another node")
	// Nor what she signed for another channel, or for a method: a message to
	// the agent, a method of the name of the channel.
	refused("signed for another channel", send(body, func(r *http.Request) {
		sign(r, []byte(openBody("other")), alice.id, 0, bob.id.Public(), MethodOpen)
	}), CodeUnauthorized, "bad signature")
	for _, method := range []string{MethodSend, "talk"} {
		refused("signed for the method "+method, send(body, func(r *http.Request) {
			sign(r, []byte(body), alice.id, 0, bob.id.Public(), method)
		}), CodeUnauthorized, "bad signature")
	}
	// Signed correctly, but by nodes the owner is not connected to: a
	// stranger, a node the owner refused, and the node itself.
	bob.trust.AddIncoming(carol.id.Public(), "carol", "", "")
	bob.trust.Reject(carol.id.Public())
	for name, id := range map[string]*identity.Identity{"stranger": identity.Generate(), "refused node": carol.id, "the node itself": bob.id} {
		refused(name, send(body, func(r *http.Request) {
			sign(r, []byte(body), id, 0, bob.id.Public(), MethodOpen)
		}), CodeUnauthorized, "not connected")
	}
	// A connected peer gets the channels that were added, and nothing else:
	// what decides about trust is no channel, whoever adds one of its name.
	for name, want := range map[string]int{"nobody/added": CodeMethodNotFound, MethodSend: CodeMethodNotFound, MethodOpen: CodeMethodNotFound, "": CodeInvalidParams} {
		body := openBody(name)
		err := send(body, func(r *http.Request) { sign(r, []byte(body), alice.id, 0, bob.id.Public(), MethodOpen) })
		if err == nil || err.Code != want {
			t.Errorf("the channel %q: got %+v, want %d", name, err, want)
		}
	}
	noParams := `{"jsonrpc":"2.0","id":1,"method":"channel/open","params":[1]}`
	refused("params that name no channel", send(noParams, func(r *http.Request) {
		sign(r, []byte(noParams), alice.id, 0, bob.id.Public(), MethodOpen)
	}), CodeInvalidParams, "invalid params")

	if onBob.ran.Load() != 1 || other.ran.Load() != 0 {
		t.Fatalf("refused requests ran the handler %d times, the other handler %d times", onBob.ran.Load()-1, other.ran.Load())
	}
	// And none of them left a connection request behind.
	if data, err := bob.trust.Snapshot(); err != nil || len(data.Incoming) != 0 {
		t.Fatalf("contacts of bob: %+v, %v", data, err)
	}
}

// Whoever opens a channel reaches the node that holds the key and nobody
// else, and what decides about trust is not opened by name.
func TestOpenReachesOnlyTheHolderOfTheKey(t *testing.T) {
	ctx := context.Background()
	alice, bob, onBob := connected(t)
	mallory, onMallory := newNode(t, "mallory"), newTalk()
	mallory.channels = []ChannelHandler{onMallory.handler("talk")}
	mallory.trust.AddTrusted(alice.id.Public(), "alice", "")

	// Mallory is a real node, trusts alice and has the channel, but answers
	// where bob is expected.
	if _, err := alice.client().Open(ctx, mallory.srv.URL, bob.id.Public(), "talk"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("got %v", err)
	}
	if _, err := alice.client().Open(ctx, plain(bob.srv), bob.id.Public(), "talk"); !errors.Is(err, errPlain) {
		t.Fatalf("without TLS: %v", err)
	}
	for _, name := range []string{MethodSend, MethodRequest, MethodAccept, MethodReject, MethodOpen} {
		if _, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), name); !errors.Is(err, ErrBuiltin) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := alice.client().Open(ctx, "://nowhere", bob.id.Public(), "talk"); err == nil {
		t.Error("an address that is none")
	}
	if onMallory.ran.Load() != 0 || onBob.ran.Load() != 0 {
		t.Fatalf("a handler ran: mallory %d, bob %d", onMallory.ran.Load(), onBob.ran.Load())
	}
	// What the node refuses comes back as its error.
	for name, want := range map[string]int{"nobody/added": CodeMethodNotFound} {
		if _, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), name); !code(err, want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := mallory.client().Open(ctx, bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeUnauthorized) {
		t.Errorf("a node bob is not connected to: %v", err)
	}
}

// A channel lives no longer than the context it was opened with.
func TestChannelEndsWithItsContext(t *testing.T) {
	alice, bob, onBob := connected(t)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := alice.client().Open(gone, bob.srv.URL, bob.id.Public(), "talk"); !errors.Is(err, context.Canceled) {
		t.Fatalf("opened with a context that is done: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), "talk")
	if err != nil {
		t.Fatal(err)
	}
	expect(t, ch, "hello "+alice.id.Public())
	cancel()
	closedWith(t, ch, context.Canceled)
	if err := onBob.why(t); err != io.EOF {
		t.Fatalf("the handler ended with %v", err)
	}
}

// What answers in place of a channel is an error, whatever it is.
func TestOpenWithoutAChannel(t *testing.T) {
	bob := identity.Generate()
	client := &Client{Identity: identity.Generate()}
	open := func(srv *httptest.Server) error {
		_, err := client.Open(context.Background(), srv.URL, bob.Public(), "talk")
		return err
	}

	if err := open(answering(t, bob, `{"jsonrpc":"2.0","id":1}`)); err == nil || !strings.Contains(err.Error(), "did not open the channel") {
		t.Errorf("an answer that is no channel: %v", err)
	}
	if err := open(answering(t, bob, "<html>")); err == nil || !strings.Contains(err.Error(), "invalid response") {
		t.Errorf("an answer that is no JSON: %v", err)
	}
	missing := serve(t, bob, http.NotFoundHandler())
	if err := open(missing); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("no node: %v", err)
	}
	// A node that hangs up, and one that does not answer at all.
	release := make(chan struct{})
	hangsUp := serve(t, bob, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer close(release)
	if err := open(hangsUp); err == nil {
		t.Error("a node that hangs up")
	}
	// Nobody waits for the answer longer than the context allows. The node
	// has the request by then: it is not the handshake that takes long.
	asked := make(chan struct{})
	silent := serve(t, bob, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(asked)
		<-release
	}))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asked
		cancel()
	}()
	if _, err := client.Open(ctx, silent.URL, bob.Public(), "talk"); !errors.Is(err, context.Canceled) {
		t.Errorf("a node that does not answer: %v", err)
	}
	missing.Close()
	if err := open(missing); err == nil {
		t.Error("nobody answers at that address")
	}
}

// A node without channels opens none, and a connection that cannot become a
// channel is an internal error.
func TestServerThatCannotOpen(t *testing.T) {
	alice, bob, onBob := connected(t)
	id := identity.Generate()
	trust := contacts.Open(bob.path)
	bare := serve(t, id, (&Server{Card: func() AgentCard { return testCard }, Identity: id, Trust: trust, Respond: echo}).Handler())
	if _, err := alice.client().Open(context.Background(), bare.URL, id.Public(), "talk"); !code(err, CodeMethodNotFound) {
		t.Fatalf("a node without channels: %v", err)
	}

	// The request did not come over a connection of its own.
	body := openBody("talk")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	sign(r, []byte(body), alice.id, 0, bob.id.Public(), MethodOpen)
	w := httptest.NewRecorder()
	bob.server.Handler().ServeHTTP(w, r)
	var res response
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil || res.Error == nil || res.Error.Code != CodeInternalError || res.Error.Message != "internal error" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if len(bob.log) != 1 || !strings.Contains(bob.log[0], "no channel talk for") || !strings.Contains(bob.log[0], alice.id.Public()) {
		t.Fatalf("log %q", bob.log)
	}
	// It does not count as a channel of alice.
	if len(bob.server.channels.count) != 0 || onBob.ran.Load() != 0 {
		t.Fatalf("channels %v, the handler ran %d times", bob.server.channels.count, onBob.ran.Load())
	}

	// Contacts that cannot be read trust nobody.
	bob.fail.state = errors.New("disk on fire")
	if _, err := alice.client().Open(context.Background(), bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeInternalError) || strings.Contains(err.Error(), "fire") {
		t.Fatalf("without contacts: %v", err)
	}
	if len(bob.log) != 2 || !strings.Contains(bob.log[1], "disk on fire") || onBob.ran.Load() != 0 {
		t.Fatalf("log %q, the handler ran %d times", bob.log, onBob.ran.Load())
	}
}

// A peer has a limited number of channels open at a time. The limit is per
// peer, and a channel that is closed makes room for another.
func TestChannelsPerPeer(t *testing.T) {
	alice, bob, _ := connected(t)
	carol := newNode(t, "carol")
	bob.trust.AddTrusted(carol.id.Public(), "carol", "")
	if bob.server.channels.limit() != MaxChannelsPerPeer {
		t.Fatalf("the limit is %d", bob.server.channels.limit())
	}
	bob.server.channels.max = 3
	ctx := context.Background()

	var channels []*Channel
	for range 3 {
		channels = append(channels, open(t, alice, bob, "talk"))
	}
	for range 2 {
		if _, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeTooManyChannels) {
			t.Fatalf("one channel too many: %v", err)
		}
	}
	// The channels that are open are not harmed, and carol has her own.
	for _, ch := range channels {
		expect(t, ch, "hello "+alice.id.Public())
	}
	expect(t, open(t, carol, bob, "talk"), "hello "+carol.id.Public())
}

// A channel that is closed makes room for another.
func TestClosedChannelMakesRoom(t *testing.T) {
	alice, bob, _ := connected(t)
	bob.server.channels.max = 2
	ctx := context.Background()
	first, second := open(t, alice, bob, "talk"), open(t, alice, bob, "talk")
	if _, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeTooManyChannels) {
		t.Fatalf("one channel too many: %v", err)
	}
	first.Close()
	eventually(t, "a closed channel should make room", func() bool {
		ch, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), "talk")
		if err != nil {
			return false
		}
		t.Cleanup(func() { ch.Close() })
		expect(t, ch, "hello "+alice.id.Public())
		return true
	})
	expect(t, second, "hello "+alice.id.Public())
}

// The node closes a channel nothing is sent over, whatever the plugins at
// its ends are waiting for.
func TestNodeClosesIdleChannel(t *testing.T) {
	alice, bob, onBob := connected(t)
	if bob.server.channels.idleTimeout() != ChannelIdleTimeout {
		t.Fatalf("the idle timeout is %v", bob.server.channels.idleTimeout())
	}
	bob.server.channels.idle = 50 * time.Millisecond
	ch := open(t, alice, bob, "talk")
	expect(t, ch, "hello "+alice.id.Public())
	closedWith(t, ch, ErrIdle)
	if err := onBob.why(t); err != ErrIdle {
		t.Fatalf("the handler ended with %v", err)
	}
	eventually(t, "an idle channel should make room", func() bool { return len(bob.server.channels.snapshot()) == 0 })
}

// snapshot is how many channels each peer has.
func (s *ChannelSet) snapshot() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	for peer, n := range s.count {
		counts[peer] = n
	}
	return counts
}

// replaceContacts makes the contacts of n the list data, in one step, as
// another process would that changes them.
func replaceContacts(t *testing.T, n *node, data contacts.Data) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(n.path+".new", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(n.path+".new", n.path); err != nil {
		t.Fatal(err)
	}
}

// When the owner is no longer connected to a peer, the channels of that peer
// are closed, and only they. Nothing tells the node that the contacts
// changed: it looks.
func TestChannelsCloseWhenTrustIsWithdrawn(t *testing.T) {
	alice, bob, onBob := connected(t)
	carol, dave := newNode(t, "carol"), newNode(t, "dave")
	bob.trust.AddTrusted(carol.id.Public(), "carol", "")
	bob.trust.AddTrusted(dave.id.Public(), "dave", "")
	if bob.server.channels.interval() != trustInterval || bob.server.channels.closeWait() != closeWait {
		t.Fatalf("checked every %v, closed after %v", bob.server.channels.interval(), bob.server.channels.closeWait())
	}
	bob.server.channels.every = 5 * time.Millisecond

	ofAlice := []*Channel{open(t, alice, bob, "talk"), open(t, alice, bob, "talk")}
	ofCarol, ofDave := open(t, carol, bob, "talk"), open(t, dave, bob, "talk")
	for _, ch := range ofAlice {
		expect(t, ch, "hello "+alice.id.Public())
	}
	expect(t, ofCarol, "hello "+carol.id.Public())
	expect(t, ofDave, "hello "+dave.id.Public())
	// While everybody is connected, the channels stay open.
	time.Sleep(50 * time.Millisecond)
	if err := ofAlice[0].Send([]byte("still here")); err != nil {
		t.Fatal(err)
	}
	expect(t, ofAlice[0], "STILL HERE")

	// The owner of bob removes alice, from another process.
	data, err := bob.trust.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	data.Trusted = data.Trusted[1:]
	replaceContacts(t, bob, data)
	for _, ch := range ofAlice {
		closedWith(t, ch, ErrDisconnected)
		if err := onBob.why(t); err != ErrDisconnected {
			t.Fatalf("the handler ended with %v", err)
		}
	}
	if _, err := alice.client().Open(context.Background(), bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeUnauthorized) {
		t.Fatalf("opening a channel without trust: %v", err)
	}
	// The others are still connected, and so are their channels.
	if err := ofCarol.Send([]byte("and me")); err != nil {
		t.Fatal(err)
	}
	expect(t, ofCarol, "AND ME")

	// The owner refuses carol. That she is still in the list of peers, as in
	// a list changed by hand, does not keep her channel open or get her a new
	// one.
	data.Blocked = []string{carol.id.Public()}
	replaceContacts(t, bob, data)
	closedWith(t, ofCarol, ErrDisconnected)
	if err := onBob.why(t); err != ErrDisconnected {
		t.Fatalf("the handler ended with %v", err)
	}
	if _, err := carol.client().Open(context.Background(), bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeUnauthorized) {
		t.Fatalf("opening a channel as a refused peer: %v", err)
	}
	if err := ofDave.Send([]byte("and me")); err != nil {
		t.Fatal(err)
	}
	expect(t, ofDave, "AND ME")

	// Contacts that cannot be read trust nobody.
	// The node may hold the lock for a moment itself, so it is taken once it
	// is free.
	lock := bob.path + ".lock"
	eventually(t, "the lock should be taken", func() bool {
		os.Remove(lock)
		return os.Mkdir(lock, 0o700) == nil
	})
	defer os.Remove(lock)
	closedWith(t, ofDave, ErrDisconnected)
	if err := onBob.why(t); err != ErrDisconnected {
		t.Fatalf("the handler ended with %v", err)
	}
	eventually(t, "no channel should be left", func() bool { return len(bob.server.channels.snapshot()) == 0 })
}

// A node that stops closes every channel, and opens no further one.
func TestServerCloseClosesChannels(t *testing.T) {
	alice, bob, onBob := connected(t)
	channels := []*Channel{open(t, alice, bob, "talk"), open(t, alice, bob, "talk")}
	for _, ch := range channels {
		expect(t, ch, "hello "+alice.id.Public())
	}
	bob.server.Close()
	for _, ch := range channels {
		closedWith(t, ch, ErrStopped)
		if err := onBob.why(t); err != ErrStopped {
			t.Fatalf("the handler ended with %v", err)
		}
	}
	if _, err := alice.client().Open(context.Background(), bob.srv.URL, bob.id.Public(), "talk"); !code(err, CodeInternalError) || !strings.Contains(err.Error(), "the node stops") {
		t.Fatalf("opening a channel of a node that stops: %v", err)
	}
	if onBob.ran.Load() != 2 {
		t.Fatalf("the handler ran %d times", onBob.ran.Load())
	}
	bob.server.Close() // again: nothing is left to close
}

// The channels a node opened itself are held in a set as well: they are
// closed with the trust in the peer and with the node.
func TestChannelSetHoldsOpenedChannels(t *testing.T) {
	alice, bob, onBob := connected(t)
	carol := newNode(t, "carol")
	carol.channels = bob.channels
	carol.trust.AddTrusted(alice.id.Public(), "alice", "")
	alice.trust.AddTrusted(bob.id.Public(), "bob", "")
	alice.trust.AddTrusted(carol.id.Public(), "carol", "")

	var opened ChannelSet
	opened.every = 5 * time.Millisecond
	hold := func(to *node) *Channel {
		t.Helper()
		ch := open(t, alice, to, "talk")
		if err := opened.Hold(ch, alice.trust); err != nil {
			t.Fatal(err)
		}
		expect(t, ch, "hello "+alice.id.Public())
		return ch
	}
	// More than a peer may open to this node: that limit is the peer's to keep.
	opened.max = 1
	toBob, second, toCarol := hold(bob), hold(bob), hold(carol)

	// Where the owner decides against a peer, its channels are closed at once.
	opened.ClosePeer(bob.id.Public())
	for _, ch := range []*Channel{toBob, second} {
		closedWith(t, ch, ErrDisconnected)
		if err := onBob.why(t); !errors.Is(err, ErrDisconnected) {
			t.Fatalf("the handler ended with %v", err)
		}
	}
	// And when the contacts change, a moment later.
	toBob = hold(bob)
	data, err := alice.trust.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	data.Trusted = data.Trusted[1:]
	replaceContacts(t, alice, data)
	closedWith(t, toBob, ErrDisconnected)

	if err := toCarol.Send([]byte("carol")); err != nil {
		t.Fatal(err)
	}
	expect(t, toCarol, "CAROL")
	opened.Close()
	closedWith(t, toCarol, ErrStopped)
	// The channel to bob is still on its way out: the check that closes it
	// runs beside the test, which only waited for bob to be told.
	eventually(t, "no channel should be left", func() bool { return len(opened.snapshot()) == 0 })

	// A set that is closed takes no channel.
	late := open(t, alice, carol, "talk")
	if err := opened.Hold(late, alice.trust); err != ErrStopped {
		t.Fatalf("got %v", err)
	}
	closedWith(t, late, ErrStopped)
}

// A channel that is being opened while the node stops is closed as soon as
// it is open.
func TestChannelOpenedWhileTheNodeStops(t *testing.T) {
	var set ChannelSet
	trust := contacts.Open(t.TempDir() + "/contacts.json")
	a, b := pair(t, time.Hour, time.Second)
	if err := set.reserve(a.peer, trust, true); err != nil {
		t.Fatal(err)
	}
	set.Close()
	set.add(a)
	closedWith(t, a, ErrStopped)
	closedWith(t, b, ErrStopped)
	if len(set.snapshot()) != 0 {
		t.Fatalf("left: %v", set.snapshot())
	}
}

// Whatever ends a channel, nothing of it is left running.
func TestChannelsLeaveNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	t.Run("channels", func(t *testing.T) {
		alice, bob, onBob := connected(t)
		bob.channels = append(bob.channels,
			ChannelHandler{Name: "once", Handle: func(_ context.Context, ch *Channel) { ch.Send([]byte("bye")) }},
			ChannelHandler{Name: "full", Handle: func(_ context.Context, ch *Channel) { ch.CloseWith("no room") }},
			ChannelHandler{Name: "deaf", Handle: func(ctx context.Context, _ *Channel) { <-ctx.Done() }},
		)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		opened := func(name string) *Channel {
			ch, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), name)
			if err != nil {
				t.Fatal(err)
			}
			return ch
		}

		opened("talk").Close() // closed by who opened it
		expect(t, opened("once"), "bye")
		closedWith(t, opened("full"), &CloseError{"no room"})
		// Abandoned by a peer that sends without end.
		stuck := opened("deaf")
		go func() {
			for stuck.Send(make([]byte, MaxChannelMessage)) == nil {
			}
		}()
		// Refused, and broken off.
		if _, err := alice.client().Open(ctx, bob.srv.URL, bob.id.Public(), "nobody/added"); !code(err, CodeMethodNotFound) {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", bob.srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		// Left open until the context of the one is done and the node of the
		// other stops.
		own, drop := context.WithCancel(ctx)
		byContext, err := alice.client().Open(own, bob.srv.URL, bob.id.Public(), "talk")
		if err != nil {
			t.Fatal(err)
		}
		byNode := opened("talk")
		expect(t, byContext, "hello "+alice.id.Public())
		expect(t, byNode, "hello "+alice.id.Public())
		drop()
		closedWith(t, byContext, context.Canceled)
		bob.server.Close()
		closedWith(t, byNode, ErrStopped)
		for range 3 {
			onBob.why(t)
		}
	})
	var now int
	for deadline := time.Now().Add(patience); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if now = runtime.NumGoroutine(); now <= before {
			return
		}
	}
	stacks := make([]byte, 1<<20)
	t.Fatalf("%d goroutines before, %d after:\n%s", before, now, stacks[:runtime.Stack(stacks, true)])
}

// The card says what the node can do: no streaming as A2A means it, and
// channels as something of its own.
func TestCardAnnouncesChannels(t *testing.T) {
	card := AgentCard{Name: "alpha", Capabilities: Capabilities{Extensions: []Extension{ChannelExtension()}}}
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`"capabilities":{"streaming":false,"pushNotifications":false,"extensions":[{"uri":%q,"description":`, ChannelExtensionURI)
	if !strings.Contains(string(b), want) || !strings.Contains(string(b), MethodOpen) || !strings.Contains(string(b), ChannelProtocol) {
		t.Fatalf("card %s", b)
	}
	if b, _ := json.Marshal(AgentCard{}); !strings.Contains(string(b), `"capabilities":{"streaming":false,"pushNotifications":false}`) {
		t.Fatalf("a card without extensions: %s", b)
	}
}
