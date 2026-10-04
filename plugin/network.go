package plugin

import (
	"context"

	"ouroboros/internal/a2a"
	"ouroboros/internal/discovery"
	"ouroboros/internal/peers"
)

type (
	// Node is a node that is online, as it announces itself.
	Node = discovery.Node
	// Peer is a node that is online with its connection to this node.
	Peer = peers.Peer
)

// How far the connection to a Peer has come.
const (
	NotConnected = peers.NotConnected
	Requested    = peers.Requested
	Connected    = peers.Connected
)

var (
	// ErrPending means the peer does not trust this node yet: a connection
	// request was left, which a human there has to accept.
	ErrPending = peers.ErrPending
	// ErrRefused means the peer refused to connect to this node.
	ErrRefused = peers.ErrRefused
	// ErrSender means the node is the peer whose message is being answered.
	ErrSender = peers.ErrSender
	// ErrNotConnected means the node is not a peer this node is connected to.
	ErrNotConnected = peers.ErrNotConnected
	// ErrUnconfirmed means the owner has not decided to connect to the node:
	// nothing was sent to it, and it waits among the connection requests.
	ErrUnconfirmed = peers.ErrUnconfirmed
	// ErrUnverified means the node that answers does not hold the key of the
	// node that was asked. Nothing was sent to it.
	ErrUnverified = a2a.ErrUnverified
)

// Label is how a node is called when talking about it.
func Label(n Node) string { return peers.Label(n) }

// NewID returns a random ID, such as the context ID of a new conversation.
func NewID() string { return a2a.NewID() }

// Network is the other nodes as a plugin reaches them. It talks to them as
// this node: every request is signed with the key of the node and sent over
// TLS pinned to the key of the node it is for. The key, the contacts and the
// decision whom to trust are not to be had from it.
type Network struct {
	net *peers.Network
}

// NetworkOf returns n as plugins see it. It is for the plugin that provides
// the network.
func NetworkOf(n *peers.Network) *Network { return &Network{net: n} }

// Find returns the nodes that are online, this node included.
func (n *Network) Find(ctx context.Context) ([]Node, error) { return n.net.Find(ctx) }

// Peers returns the nodes that are online with their connection to this node.
func (n *Network) Peers(ctx context.Context) ([]Peer, error) { return n.net.Peers(ctx) }

// Resolve turns a node ID, ID prefix, name or host:port into a node.
func (n *Network) Resolve(ctx context.Context, target string) (Node, error) {
	return n.net.Resolve(ctx, target)
}

// Send delivers text to the agent of node within the conversation contextID
// and returns its answer. The text only goes to a connected peer. A node the
// owner never decided on is proposed to the owner instead (ErrUnconfirmed):
// no plugin makes this node trust another one. A node the owner asked to
// connect, where no human has decided yet, is reported as ErrPending.
func (n *Network) Send(ctx context.Context, node Node, contextID, text string) (string, error) {
	return n.net.Send(ctx, node, contextID, text)
}

// Call runs method on node, a Method that a plugin added there, and returns
// the message it answers with: nil if it answers with none. params is sent as
// JSON; an error the method answers with is an *Error.
//
// It only reaches connected peers: for any other node nothing is sent, and
// Call fails with ErrNotConnected. The call is signed for the node and the
// method like every message, and only the holder of the key of node gets it.
func (n *Network) Call(ctx context.Context, node Node, method string, params any) (*Message, error) {
	return n.net.Call(ctx, node, method, params)
}
