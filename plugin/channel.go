package plugin

import (
	"context"

	"ouroboros/internal/a2a"
)

// A method is one request with one answer, of at most 1 MiB. What flows goes
// over a channel: audio, a terminal, events, a large file. One plugin adds a
// ChannelHandler to its node, the plugin of a connected peer opens it with
// Network.Open, and both hold a *Channel until one of them closes it.
//
// A plugin never signs and never decides whom to trust: ouro opens the
// channel, with the same checks as for a Method, and keeps its limits.
type (
	// ChannelHandler is a channel added to the node, for a plugin on a
	// connected peer to open with Network.Open. Its Handle runs once for
	// every channel that is opened under its Name, with the channel open in
	// both directions; when Handle returns, ouro closes the channel. The
	// context is done as soon as the channel is closed by anyone.
	//
	// Handle runs only for an opening request that the peer signed for this
	// node, and only if the owner of this node is connected to the peer:
	// everyone else is refused before Handle is reached, and Peer of the
	// channel is who signed. A name that is built in is never opened.
	ChannelHandler = a2a.ChannelHandler

	// Channel is one end of a channel between two connected nodes. Both ends
	// have the same:
	//
	//	Send(msg []byte) error       one message, of at most MaxMessage bytes
	//	Receive() ([]byte, error)    the next message of the peer, whole
	//	Close() error                the peer reads io.EOF after what was sent
	//	CloseWith(reason string) error  the peer reads a *ChannelClosed instead
	//	Context() context.Context    done as soon as the channel is closed
	//	Peer() string                the public key of the node on the other side
	//
	// Messages arrive whole and in the order they were sent. Send waits while
	// the peer does not receive, so nothing piles up. Once the channel is
	// closed, Send and Receive return why: io.EOF if the peer closed it,
	// ErrChannelClosed if this side did, a *ChannelClosed with the reason
	// that was given, or the error of a connection that broke.
	Channel = a2a.Channel

	// ChannelClosed is the error of a channel that was closed with a reason:
	// by the plugin on the other side with CloseWith, or by ouro with one of
	// ErrIdle, ErrDisconnected and ErrStopped (compare with errors.Is).
	ChannelClosed = a2a.CloseError
)

// The limits of a channel. ouro keeps them: a plugin cannot change them.
const (
	// MaxMessage is how many bytes one message of a channel may have. What
	// is larger is sent in pieces.
	MaxMessage = a2a.MaxChannelMessage
	// MaxChannelsPerPeer is how many channels one peer may have open to a
	// node at a time. One more is refused with CodeTooManyChannels.
	MaxChannelsPerPeer = a2a.MaxChannelsPerPeer
	// ChannelIdleTimeout is how long a channel stays open without a message
	// in either direction.
	ChannelIdleTimeout = a2a.ChannelIdleTimeout

	// CodeTooManyChannels is the code of the Error for a peer that has
	// MaxChannelsPerPeer channels open already.
	CodeTooManyChannels = a2a.CodeTooManyChannels
)

var (
	// ErrChannelClosed is the error of Send and Receive on a channel that
	// this side has closed.
	ErrChannelClosed = a2a.ErrChannelClosed
	// ErrTooLarge is the error of Send for a message of more than MaxMessage
	// bytes. The channel stays open.
	ErrTooLarge = a2a.ErrTooLarge
	// ErrIdle closes a channel nothing was sent over for ChannelIdleTimeout.
	ErrIdle error = a2a.ErrIdle
	// ErrDisconnected closes the channels of a peer the owner of one of the
	// two nodes is no longer connected to.
	ErrDisconnected error = a2a.ErrDisconnected
	// ErrStopped closes the channels of a node that stops.
	ErrStopped error = a2a.ErrStopped
)

// Open opens the channel name on node, a ChannelHandler that a plugin added
// there, and returns this end of it. An error the node answers with instead
// is an *Error: CodeMethodMissing if it has no such channel,
// CodeUnauthorized if its owner is not connected to this node,
// CodeTooManyChannels if this node has too many open there.
//
// It only reaches connected peers: for any other node nothing is sent, and
// Open fails with ErrNotConnected. The opening request is signed for the
// node like every message, and the channel is the connection it went over:
// TLS that only the holder of the key of node can answer.
//
// The channel lives no longer than ctx: it is closed when ctx is done. Pass a
// context of its own for a channel that outlives the command or the tool
// that opens it. ouro closes it as well when the owner is no longer
// connected to the peer, after ChannelIdleTimeout without a message, and
// when the node stops.
func (n *Network) Open(ctx context.Context, node Node, name string) (*Channel, error) {
	return n.net.Open(ctx, node, name)
}
