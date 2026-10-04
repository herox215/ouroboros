package a2a

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// A channel is the second way two connected nodes talk. A method is one
// request with one answer; a channel stays open and carries messages in both
// directions until one side closes it: audio, a terminal, events, a file that
// is larger than a request may be.
//
// It is opened with the built-in method MethodOpen, a request like every
// other: signed by the caller for this node, checked for its age and its
// nonce, and only taken from a peer the owner is connected to. The node does
// not answer it with a JSON-RPC response but with "101 Switching Protocols",
// and from then on the TLS connection the request came over is the channel.
// So the channel is encrypted and pinned like every request, and what a
// plugin gets is open already: it neither signs nor decides whom to trust.
//
// A2A has message/stream for what flows, but that is server-sent events: one
// direction, text, one answer to one message. A channel needs both
// directions at once, so it is no part of A2A and is announced in the agent
// card as an extension (ChannelExtension), not as streaming.
//
// On the connection both sides send frames:
//
//	1 byte   the kind: 0 a message, 1 the end of the channel
//	4 bytes  the length of what follows, big endian, at most MaxChannelMessage
//	n bytes  the message; for the end the reason as text, empty for none
//
// The limits belong to the frame of ouro, not to a plugin: see ChannelSet.

// ChannelProtocol is what the connection is upgraded to when a channel opens.
const ChannelProtocol = "ouro-channel/1"

// ChannelExtensionURI names channels in the agent card.
const ChannelExtensionURI = "https://github.com/herox215/ouroboros#channels"

// The limits of a channel.
const (
	// MaxChannelsPerPeer is how many channels one peer may have open to this
	// node at a time. A further one is refused with CodeTooManyChannels.
	MaxChannelsPerPeer = 8
	// ChannelIdleTimeout is how long a channel stays open without a message
	// in either direction. Then it is closed with ErrIdle.
	ChannelIdleTimeout = 5 * time.Minute
	// MaxChannelMessage is how long one message of a channel may be. What is
	// larger is sent in pieces.
	MaxChannelMessage = 1 << 20

	// trustInterval is how often the node looks whether the peers it has
	// channels with are still connected. The contacts are a file that other
	// processes change, so nothing tells the node: it has to look.
	trustInterval = time.Second
	// closeWait is how long a channel that is closed waits for the peer to
	// take what was sent before the connection is cut.
	closeWait = 2 * time.Second
	// maxReason is how long the reason of a close may be, in bytes.
	maxReason = 256
)

// The kinds of frames.
const (
	frameMessage = 0
	frameClose   = 1
	frameHeader  = 5 // bytes: kind and length
)

// CloseError is why a channel was closed, if a reason was given: by the
// plugin on the other side with CloseWith, or by ouro on either side with
// one of ErrIdle, ErrDisconnected and ErrStopped. Compare with errors.Is.
type CloseError struct {
	Reason string
}

func (e *CloseError) Error() string { return "a2a: channel closed: " + e.Reason }

// Is makes two CloseErrors with the same reason the same error: a reason
// that came over the network is no pointer this side knows.
func (e *CloseError) Is(target error) bool {
	t, ok := target.(*CloseError)
	return ok && t.Reason == e.Reason
}

var (
	// ErrChannelClosed is the error of Send and Receive on a channel that
	// this side has closed.
	ErrChannelClosed = errors.New("a2a: channel is closed")
	// ErrTooLarge is the error of Send for a message of more than
	// MaxChannelMessage bytes. The channel stays open.
	ErrTooLarge = errors.New("a2a: message is too large for a channel, send it in pieces")
	// ErrIdle closes a channel nothing was sent over for ChannelIdleTimeout.
	ErrIdle = &CloseError{"idle for too long"}
	// ErrDisconnected closes the channels of a peer the owner is no longer
	// connected to.
	ErrDisconnected = &CloseError{"not connected"}
	// ErrStopped closes the channels of a node that stops.
	ErrStopped = &CloseError{"node stopped"}

	// errFrame is what a peer that does not send frames breaks a channel with.
	errFrame = errors.New("a2a: invalid frame on a channel")
)

// ChannelHandler is a channel added to the server: the receiving side. Handle
// runs once for every channel a peer opens under Name, with the channel open
// in both directions; when it returns, the channel is closed. ctx is done as
// soon as the channel is closed by anyone.
//
// Like a Method it only runs for a peer the owner is connected to, whose
// opening request was signed for this node: Handle never runs for anyone
// else, and ch.Peer is who signed. A name that is built in (see Builtin) is
// never opened.
//
// To add a channel: contribute a ChannelHandler from a plugin with
// plugin.Add; a peer opens it with Open of the network (peers.Network,
// Client.Open).
type ChannelHandler struct {
	Name   string
	Handle func(ctx context.Context, ch *Channel)
}

// channelParams are the parameters of MethodOpen.
type channelParams struct {
	Name string `json:"name"`
}

// Channel is one end of a channel between two connected nodes. Both ends are
// the same: each sends and receives messages until one of them closes it.
// A Channel may be used by several goroutines at once.
//
// Once the channel is closed, Send and Receive return why: io.EOF if the peer
// closed it, ErrChannelClosed if this side did, a *CloseError if a reason was
// given, or the error of the connection that broke.
type Channel struct {
	peer   string
	conn   net.Conn
	ctx    context.Context
	cancel context.CancelCauseFunc
	wait   time.Duration // closeWait

	msgs    chan []byte   // from the reader to Receive, one at a time
	closing chan struct{} // closed when the channel is: nothing is delivered any more
	gone    chan struct{} // closed when the reader has ended

	send sync.Mutex // one frame at a time
	torn bool       // half a frame may be out: nothing can follow it

	mu        sync.Mutex
	cause     error // why it was closed; nil while it is open
	idle      *time.Timer
	idleAfter time.Duration
	finished  bool
	after     []func() // run once the channel is closed
}

// newChannel returns the channel with peer on conn, read through r, that is
// closed after idle without a message.
func newChannel(conn net.Conn, r io.Reader, peer string, idle, wait time.Duration) *Channel {
	c := &Channel{
		peer: peer, conn: conn, wait: wait, idleAfter: idle,
		msgs: make(chan []byte), closing: make(chan struct{}), gone: make(chan struct{}),
	}
	c.ctx, c.cancel = context.WithCancelCause(context.Background())
	c.mu.Lock() // the timer reads what it sets
	c.idle = time.AfterFunc(idle, func() { c.shut(ErrIdle) })
	c.mu.Unlock()
	go c.read(r)
	return c
}

// Peer is the public key of the node on the other side: the key its opening
// request was signed with, or the key the connection to it is pinned to.
func (c *Channel) Peer() string { return c.peer }

// Context is done as soon as the channel is closed, by either side or by
// ouro. Its cause (context.Cause) is why.
func (c *Channel) Context() context.Context { return c.ctx }

// Send sends one message to the peer, which gets it whole from one Receive.
// It waits while the peer does not take what was sent. A message may be
// empty, and at most MaxChannelMessage bytes long.
func (c *Channel) Send(msg []byte) error {
	if len(msg) > MaxChannelMessage {
		return ErrTooLarge
	}
	c.send.Lock()
	defer c.send.Unlock()
	if err := c.err(); err != nil {
		return err
	}
	if err := writeFrame(c.conn, frameMessage, msg); err != nil {
		c.torn = true
		c.end(err)
		return c.err()
	}
	c.touch()
	return nil
}

// Receive returns the next message of the peer. It waits for one, and fails
// once the channel is closed: with io.EOF if the peer closed it, after the
// messages it sent before.
func (c *Channel) Receive() ([]byte, error) {
	select {
	case msg := <-c.msgs:
		return msg, nil
	case <-c.closing:
		return nil, c.err()
	}
}

// Close closes the channel: the peer reads io.EOF after what was sent to it,
// for which Close waits a moment. Closing a closed channel does nothing.
func (c *Channel) Close() error {
	c.shut(ErrChannelClosed)
	return nil
}

// CloseWith closes the channel and tells the peer why: its Send and Receive
// fail with a *CloseError with that reason, of which the first 256 bytes
// arrive.
func (c *Channel) CloseWith(reason string) error {
	if reason = cut(reason); reason == "" {
		return c.Close()
	}
	c.shut(&CloseError{reason})
	return nil
}

// cut shortens the reason of a close to maxReason.
func cut(reason string) string {
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	return strings.ToValidUTF8(reason, "")
}

// err is why the channel was closed, nil while it is open.
func (c *Channel) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}

// touch notes that a message went over the channel: it is not idle.
func (c *Channel) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cause == nil {
		c.idle.Reset(c.idleAfter)
	}
}

// read takes the frames of the peer off the connection and hands its
// messages to Receive one at a time: while nobody receives, nothing more is
// read, and the peer waits in Send. It ends with the connection.
func (c *Channel) read(r io.Reader) {
	defer close(c.gone)
	for {
		kind, payload, err := readFrame(r)
		switch {
		case err == io.EOF:
			// io.EOF is for a channel the peer closed, not for one that broke.
			c.end(io.ErrUnexpectedEOF)
			return
		case err != nil:
			c.end(err)
			return
		case kind == frameClose:
			if reason := cut(string(payload)); reason != "" {
				c.end(&CloseError{reason})
			} else {
				c.end(io.EOF)
			}
			return
		}
		c.touch()
		select {
		case c.msgs <- payload:
		case <-c.closing: // closed on this side: what still arrives is dropped
		}
	}
}

// mark notes that the channel is closed because of cause, and reports
// whether it was open: who closes first says why.
func (c *Channel) mark(cause error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cause != nil {
		return false
	}
	c.cause = cause
	c.idle.Stop()
	c.cancel(cause)
	close(c.closing)
	return true
}

// end closes a channel there is nobody left to tell: the peer closed it, or
// the connection broke.
func (c *Channel) end(cause error) {
	if c.mark(cause) {
		c.finish()
	}
}

// shut closes the channel from this side because of cause and tells the
// peer. What was sent before is on its way: the connection is only cut when
// the peer has hung up, or after closeWait. A Send the peer does not take
// has the same time to get through.
func (c *Channel) shut(cause error) {
	if !c.mark(cause) {
		return
	}
	if c.tell(cause) {
		select {
		case <-c.gone:
		case <-time.After(c.wait):
		}
	}
	c.finish()
	<-c.gone
}

// tell sends the peer the frame that ends the channel, with the reason of
// cause if it has one, and reports whether it went out. It goes after the
// message a Send is sending. If the peer does not take that within closeWait,
// the Send fails and nothing can follow it: the peer then finds the
// connection cut.
func (c *Channel) tell(cause error) bool {
	c.conn.SetWriteDeadline(time.Now().Add(c.wait))
	c.send.Lock()
	defer c.send.Unlock()
	if c.torn {
		return false
	}
	var reason string
	if closed, ok := cause.(*CloseError); ok {
		reason = closed.Reason
	}
	return writeFrame(c.conn, frameClose, []byte(reason)) == nil
}

// finish releases what a closed channel held.
func (c *Channel) finish() {
	c.conn.Close()
	c.mu.Lock()
	after := c.after
	c.after, c.finished = nil, true
	c.mu.Unlock()
	for _, f := range after {
		f()
	}
}

// onClose makes the channel call f once it is closed; at once if it is.
func (c *Channel) onClose(f func()) {
	c.mu.Lock()
	if !c.finished {
		c.after = append(c.after, f)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	f()
}

// writeFrame writes one frame in one piece.
func writeFrame(w io.Writer, kind byte, payload []byte) error {
	frame := make([]byte, frameHeader, frameHeader+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	_, err := w.Write(append(frame, payload...))
	return err
}

// readFrame reads one frame. It fails with io.EOF if the connection ended
// before one began.
func readFrame(r io.Reader) (kind byte, payload []byte, err error) {
	var header [frameHeader]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if header[0] > frameClose || size > MaxChannelMessage {
		return 0, nil, errFrame
	}
	payload = make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

// ChannelSet are the open channels of one side of a node: the ones peers
// opened to its Server, or the ones it opened to peers. It is where the
// limits of the frame are kept, whatever the plugins at the ends do:
//
//   - a peer has at most MaxChannelsPerPeer channels open to the node,
//   - a channel without a message for ChannelIdleTimeout is closed,
//   - the channels of a peer the owner is no longer connected to, because it
//     was removed from the contacts or refused, are closed at the latest
//     trustInterval after the contacts changed,
//   - Close closes them all, for a node that stops.
//
// The zero value is ready.
type ChannelSet struct {
	mu      sync.Mutex
	trust   Trust
	count   map[string]int // channels by peer, with the ones that are being opened
	open    map[*Channel]struct{}
	stopped bool
	timer   *time.Timer // runs check while channels are open

	// The limits, for tests to shorten: zero stands for the constants.
	max               int
	idle, every, wait time.Duration
}

func (s *ChannelSet) limit() int { return cmp.Or(s.max, MaxChannelsPerPeer) }

func (s *ChannelSet) idleTimeout() time.Duration { return cmp.Or(s.idle, ChannelIdleTimeout) }

func (s *ChannelSet) interval() time.Duration { return cmp.Or(s.every, trustInterval) }

func (s *ChannelSet) closeWait() time.Duration { return cmp.Or(s.wait, closeWait) }

// reserve makes room for one more channel with peer, whom trust decides
// about, and counts it. limited says whether MaxChannelsPerPeer applies.
// Whoever reserved calls add with the channel, or release without one.
func (s *ChannelSet) reserve(peer string, trust Trust, limited bool) *Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.stopped:
		return &Error{CodeInternalError, "the node stops"}
	case limited && s.count[peer] >= s.limit():
		return &Error{CodeTooManyChannels, "too many channels are open for you, close one first"}
	}
	if s.count == nil {
		s.count, s.open = map[string]int{}, map[*Channel]struct{}{}
	}
	s.trust = trust
	s.count[peer]++
	return nil
}

// release gives back what reserve counted for peer.
func (s *ChannelSet) release(peer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count[peer]--; s.count[peer] == 0 {
		delete(s.count, peer)
	}
}

// add takes ch, a channel that room was reserved for, until it is closed.
func (s *ChannelSet) add(ch *Channel) {
	s.mu.Lock()
	s.open[ch] = struct{}{}
	stopped := s.stopped
	if s.timer == nil {
		s.timer = time.AfterFunc(s.interval(), s.check)
	}
	s.mu.Unlock()
	ch.onClose(func() { s.remove(ch) })
	if stopped { // while the channel was being opened
		ch.shut(ErrStopped)
	}
}

// remove forgets ch, which is closed.
func (s *ChannelSet) remove(ch *Channel) {
	s.release(ch.peer)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.open, ch)
	if len(s.open) == 0 {
		s.timer.Stop()
		s.timer = nil
	}
}

// Hold takes ch, a channel this node opened to a peer whom trust decides
// about: from now on it is closed when the peer is no longer connected, and
// when the set is closed. A set that is closed takes no channel: ch is closed
// and Hold fails with ErrStopped.
func (s *ChannelSet) Hold(ch *Channel, trust Trust) error {
	if s.reserve(ch.peer, trust, false) != nil {
		ch.shut(ErrStopped)
		return ErrStopped
	}
	s.add(ch)
	return nil
}

// check closes the channels of the peers that are no longer connected: the
// ones that are not trusted any more, and the ones the owner refused. It
// comes back while channels are open. Contacts that cannot be read trust
// nobody.
func (s *ChannelSet) check() {
	s.mu.Lock()
	trust := s.trust
	peers := make([]string, 0, len(s.count))
	for peer := range s.count {
		peers = append(peers, peer)
	}
	s.mu.Unlock()
	for _, peer := range peers {
		if trusted, blocked, err := trust.State(peer); err != nil || !trusted || blocked {
			s.ClosePeer(peer)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Reset(s.interval())
	}
}

// ClosePeer closes every channel with the peer with publicKey, which is told
// ErrDisconnected. It is for where the owner withdraws trust.
func (s *ChannelSet) ClosePeer(publicKey string) {
	s.shut(ErrDisconnected, func(ch *Channel) bool { return ch.peer == publicKey })
}

// Close closes every channel, whose peers are told ErrStopped, and takes no
// further one: it is for the node that stops.
func (s *ChannelSet) Close() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.shut(ErrStopped, func(*Channel) bool { return true })
}

// shut closes the channels that which picks because of cause, and waits
// until they are closed.
func (s *ChannelSet) shut(cause error, which func(*Channel) bool) {
	s.mu.Lock()
	var picked []*Channel
	for ch := range s.open {
		if which(ch) {
			picked = append(picked, ch)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, ch := range picked {
		wg.Go(func() { ch.shut(cause) })
	}
	wg.Wait()
}
