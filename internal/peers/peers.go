// Package peers is the other nodes as this node sees them: who is online,
// who is connected, and how to talk to them.
//
// Everything that reaches another node goes through Network: Find and Peers
// list them, Resolve and Online pick one, Send talks to it, Call runs a method a plugin
// added to it, Open opens a channel a plugin added to it, Accept and Reject
// decide on its connection request. Front ends and tools call these and never
// use discovery, a2a or contacts directly.
package peers

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
	"ouroboros/internal/identity"
)

// notifyTimeout is how long a peer may take to hear about a decision.
const notifyTimeout = 10 * time.Second

var (
	// ErrPending means the peer does not trust this node yet. A connection
	// request was left, which a human there has to accept.
	ErrPending = errors.New("connection request sent, waiting for approval")
	// ErrRefused means the peer refused to connect to this node.
	ErrRefused = errors.New("refused to connect")
	// ErrSender means the node is the peer whose message is being answered:
	// it gets the answer, it is not asked in turn.
	ErrSender = errors.New("is the peer you are answering, reply to it instead of asking it")
	// ErrNotConnected means the node is not a peer this node is connected
	// to: nothing was sent to it.
	ErrNotConnected = errors.New("is not connected to this node")
	// ErrUnconfirmed means the node is one the owner of this node has not
	// decided to connect to: nothing was sent to it. It waits among the
	// connection requests for the owner to confirm.
	ErrUnconfirmed = errors.New("is not connected: the owner has to confirm the connection first, among the connection requests")
	// ErrNoRequest means there is no such connection request to decide on.
	ErrNoRequest = errors.New("request is gone")
	// ErrNotSaved means a decision on a connection request could not be
	// stored. Nothing has changed: the request is still waiting.
	ErrNotSaved = errors.New("contact could not be saved")
)

// Finder finds the nodes that are online. discovery.Scanner is one; tests
// pass a fixed list.
type Finder interface {
	Find(ctx context.Context) ([]discovery.Node, error)
}

// Network reaches the other nodes on behalf of this one.
type Network struct {
	Finder   Finder
	Contacts *contacts.Store
	// Client returns the client that signs requests as this node.
	Client func() (*a2a.Client, error)

	channels a2a.ChannelSet // the channels this node opened to peers
}

// State is how far the connection to a peer has come.
type State int

const (
	NotConnected State = iota
	Requested          // this node asked, the human there has not decided
	Connected
)

func (s State) String() string {
	switch s {
	case Requested:
		return "connection requested"
	case Connected:
		return "connected"
	}
	return "not connected"
}

// Peer is a node that is online, as seen from this node.
type Peer struct {
	discovery.Node
	State State
	// About is what a connected peer says about itself: its claim, nothing more.
	About string
}

// Label is how a node is called when talking about it.
func Label(n discovery.Node) string { return cmp.Or(n.Name, n.Addr()) }

// Find returns the nodes that are online, this node included.
func (n *Network) Find(ctx context.Context) ([]discovery.Node, error) {
	return n.Finder.Find(ctx)
}

// Peers returns the nodes that are online with their connection to this node.
func (n *Network) Peers(ctx context.Context) ([]Peer, error) {
	nodes, err := n.Find(ctx)
	if err != nil {
		return nil, err
	}
	known, err := n.Contacts.Snapshot()
	if err != nil {
		return nil, err
	}
	peers := make([]Peer, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		p := &peers[i]
		p.Node = node
		switch {
		case slices.ContainsFunc(known.Trusted, func(c contacts.Contact) bool { return c.PublicKey == node.PublicKey }):
			// Anyone can announce the key of a trusted peer. Connected is only
			// the node that proves to hold it, and only that gets a word in.
			wg.Go(func() {
				if about, ok := describe(ctx, node); ok {
					p.State, p.About = Connected, about
				}
			})
		case slices.ContainsFunc(known.Outgoing, func(r contacts.Request) bool { return r.PublicKey == node.PublicKey }):
			p.State = Requested
		}
	}
	wg.Wait()
	return peers, nil
}

// Resolve turns a node ID, ID prefix, name or host:port into a node.
func (n *Network) Resolve(ctx context.Context, target string) (discovery.Node, error) {
	if strings.Contains(target, ":") {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return discovery.Node{}, err
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			return discovery.Node{}, fmt.Errorf("invalid port in %q", target)
		}
		return discovery.Node{Host: host, Port: p}, nil
	}
	return n.Online(ctx, target)
}

// Online turns a node ID, ID prefix or name into one of the nodes that are
// online. Unlike Resolve it takes no address: it is for whoever must not send
// this node to an address of its choosing, such as an agent.
func (n *Network) Online(ctx context.Context, target string) (discovery.Node, error) {
	nodes, err := n.Find(ctx)
	if err != nil {
		return discovery.Node{}, err
	}
	return match(nodes, target)
}

func match(nodes []discovery.Node, target string) (discovery.Node, error) {
	var hits []discovery.Node
	for _, n := range nodes {
		if n.ID == target {
			return n, nil
		}
		if n.Name == target || strings.HasPrefix(n.ID, target) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 0:
		return discovery.Node{}, fmt.Errorf("no node %q found (see: ouro list)", target)
	case 1:
		return hits[0], nil
	}
	ids := make([]string, len(hits))
	for i, n := range hits {
		ids[i] = n.ID
	}
	return discovery.Node{}, fmt.Errorf("%q is ambiguous: %s", target, strings.Join(ids, ", "))
}

// NameOf returns the name of the trusted peer that target names by its ID
// or the start of it, as an agent may name a peer. It is "" for a target
// that names no trusted peer, or more than one, and when the contacts cannot
// be read. It asks nobody on the network.
func (n *Network) NameOf(target string) string {
	known, err := n.Contacts.Snapshot()
	if err != nil || target == "" {
		return ""
	}
	var names []string
	for _, c := range known.Trusted {
		if strings.HasPrefix(identity.IDOf(c.PublicKey), target) {
			names = append(names, c.Name)
		}
	}
	if len(names) != 1 {
		return ""
	}
	return names[0]
}

// Send delivers text to the agent of node within the conversation contextID
// and returns its answer.
//
// The text only goes to a peer the owner of this node is connected to. Any
// other node gets nothing of it:
//
//   - A node the owner never decided on is proposed to the owner (see
//     contacts.Propose), and Send fails with ErrUnconfirmed. Only when the
//     owner accepts that, the node is asked to connect: no agent and no plugin
//     makes this node trust another one.
//   - A node the owner asked to connect is asked again, without the text. As
//     long as a human there has not decided, Send fails with ErrPending; once
//     the node says that it is connected, it is trusted and gets the text.
//   - A node the owner refused is not asked: ErrNotConnected. Neither is any
//     node that is not connected while a peer is being answered (see
//     a2a.Origin): a peer does not make this node ask for connections.
//
// While a peer is being answered, that peer cannot be the target: two agents
// asking each other would never finish.
//
// Whatever is sent is signed for the public key of node and sent over a
// connection pinned to that key: whoever else answers at its address gets
// nothing to read, and Send fails with a2a.ErrUnverified.
func (n *Network) Send(ctx context.Context, node discovery.Node, contextID, text string) (string, error) {
	origin, answering := a2a.OriginFrom(ctx)
	// A node without a key cannot be told apart from the sender.
	if answering && (node.PublicKey == "" || node.PublicKey == origin.PublicKey) {
		return "", ErrSender
	}
	client, err := n.Client()
	if err != nil {
		return "", err
	}
	publicKey := node.PublicKey
	if publicKey == "" {
		// A node given by its address: the key of whoever answers there, and
		// only the holder of that key is reached from here on.
		if publicKey, err = key(ctx, node); err != nil {
			return "", err
		}
	}
	known, err := n.Contacts.Snapshot()
	if err != nil {
		return "", err
	}
	req := contacts.Request{PublicKey: publicKey, Name: node.Name, Model: node.Model, Addr: node.Addr()}
	// The node itself needs no connection: the owner writes to its own agent.
	own := publicKey == client.Identity.Public()
	if !own && !slices.ContainsFunc(known.Trusted, func(c contacts.Contact) bool { return c.PublicKey == publicKey }) {
		switch {
		case answering || slices.Contains(known.Blocked, publicKey):
			return "", fmt.Errorf("%s %w", Label(node), ErrNotConnected)
		case !slices.ContainsFunc(known.Outgoing, func(r contacts.Request) bool { return r.PublicKey == publicKey }):
			if err := n.Contacts.Propose(req.PublicKey, req.Name, req.Model, req.Addr); err != nil {
				return "", err
			}
			return "", fmt.Errorf("%s %w", Label(node), ErrUnconfirmed)
		}
		// The owner asked the node to connect. Has it decided by now?
		if err := n.request(ctx, client, req); err != nil {
			return "", err
		}
	}
	reply, err := client.Send(ctx, url(req.Addr), publicKey, contextID, text)
	switch {
	case a2a.IsPending(err):
		// A peer of this node that does not trust it, or no longer. Remembered,
		// so that the acceptance of the peer is recognised later.
		if err := n.Contacts.AddOutgoing(req.PublicKey, req.Name, req.Model, req.Addr); err != nil {
			return "", fmt.Errorf("%w, but it could not be remembered here: %w", ErrPending, err)
		}
		return "", ErrPending
	case a2a.IsBlocked(err):
		return "", ErrRefused
	}
	return reply, err
}

// request asks the peer of req to connect, which the owner of this node
// decided to do, and tells it nothing else. It returns nil once the peer is
// connected: a peer that says it trusts this node is trusted in turn, like
// one that accepts. Until a human there has decided it fails with ErrPending,
// and with ErrRefused if the peer refused.
func (n *Network) request(ctx context.Context, client *a2a.Client, req contacts.Request) error {
	err := client.Request(ctx, url(req.Addr), req.PublicKey)
	switch {
	case err == nil:
		return n.Contacts.AddTrusted(req.PublicKey, req.Name, req.Model)
	case a2a.IsPending(err):
		return ErrPending
	case a2a.IsBlocked(err):
		// Nothing to wait for any more. If it cannot be dropped, it is asked again.
		n.Contacts.RemoveOutgoing(req.PublicKey)
		return ErrRefused
	}
	return err
}

// Call runs method on node, a method a plugin added there (see a2a.Method),
// and returns the message it answers with: nil if it answers with none.
// params is sent as JSON; an error the method answers with is an *a2a.Error.
//
// It is for connected peers only: for a node this node does not trust nothing
// is sent, and Call fails with ErrNotConnected. To connect, the owners talk
// first (Send, Accept). The other side checks the same: it runs added methods
// for peers its owner trusts, and for nobody else.
//
// The call is signed and pinned like Send: for the public key of node and
// the method, over a connection only the holder of that key can answer.
func (n *Network) Call(ctx context.Context, node discovery.Node, method string, params any) (*a2a.Message, error) {
	publicKey, client, err := n.connected(ctx, node)
	if err != nil {
		return nil, err
	}
	return client.Call(ctx, url(node.Addr()), publicKey, method, params)
}

// Open opens the channel name on node, a channel a plugin added there (see
// a2a.ChannelHandler), and returns this end of it: both sides send and
// receive messages until one of them closes it. An error the node answers
// with instead is an *a2a.Error.
//
// Like Call it is for connected peers only and fails with ErrNotConnected
// for any other node, with nothing sent; and it is signed and pinned like
// Call: the opening request for the public key of node, over a connection
// only the holder of that key can answer, which then is the channel.
//
// The channel lives no longer than ctx. Whatever the plugin does with it, it
// is closed when the owner is no longer connected to the peer, when nothing
// was sent over it for a2a.ChannelIdleTimeout, and by Close.
func (n *Network) Open(ctx context.Context, node discovery.Node, name string) (*a2a.Channel, error) {
	publicKey, client, err := n.connected(ctx, node)
	if err != nil {
		return nil, err
	}
	ch, err := client.Open(ctx, url(node.Addr()), publicKey, name)
	if err != nil {
		return nil, err
	}
	if err := n.channels.Hold(ch, n.Contacts); err != nil {
		return nil, err
	}
	return ch, nil
}

// Close closes every channel this node opened to a peer, and from then on
// Open fails. It is for the node that stops.
func (n *Network) Close() { n.channels.Close() }

// connected returns the public key of node and the client to reach it with,
// if node is a peer this node is connected to, and ErrNotConnected if not.
func (n *Network) connected(ctx context.Context, node discovery.Node) (publicKey string, client *a2a.Client, err error) {
	if publicKey = node.PublicKey; publicKey == "" {
		// A node given by its address is whoever answers there. It is reached
		// only if that is a peer, and only that peer is.
		if publicKey, err = key(ctx, node); err != nil {
			return "", nil, err
		}
	}
	trusted, _, err := n.Contacts.State(publicKey)
	if err != nil {
		return "", nil, err
	}
	if !trusted {
		return "", nil, fmt.Errorf("%s %w", Label(node), ErrNotConnected)
	}
	if client, err = n.Client(); err != nil {
		return "", nil, err
	}
	return publicKey, client, nil
}

// Accept trusts the peer that sent the connection request and tells it so.
// A decision that could not be stored fails with ErrNotSaved and is not told
// to the peer.
//
// A request that the agent of this node proposed (see Send) is not one of a
// peer: accepting it asks the peer to connect, and a human there decides.
func (n *Network) Accept(ctx context.Context, publicKey string) error {
	req, ok, err := n.Contacts.Approve(publicKey)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSaved, err)
	}
	if !ok {
		return ErrNoRequest
	}
	if req.Proposed {
		return n.ask(ctx, req)
	}
	return n.notify(ctx, req.Addr, publicKey, (*a2a.Client).Accept)
}

// ask sends the peer of req the connection request the owner agreed to. The
// decision stands, like in notify: a peer that could not be asked now is
// asked again by the next Send to it.
func (n *Network) ask(ctx context.Context, req contacts.Request) error {
	client, err := n.Client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	if err := n.request(ctx, client, req); err != nil && !errors.Is(err, ErrPending) {
		return fmt.Errorf("decided locally, but %s was not asked: %w", req.Addr, err)
	}
	return nil
}

// Reject refuses the connection request of a peer and tells it so. Like
// Accept it fails with ErrNotSaved if the decision could not be stored. With
// the decision every channel this node opened to the peer is closed.
//
// A request that the agent of this node proposed is dropped, and the peer
// hears nothing: it never asked.
func (n *Network) Reject(ctx context.Context, publicKey string) error {
	req, ok, err := n.Contacts.Reject(publicKey)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSaved, err)
	}
	if !ok {
		return ErrNoRequest
	}
	if req.Proposed {
		return nil
	}
	n.channels.ClosePeer(publicKey)
	return n.notify(ctx, req.Addr, publicKey, (*a2a.Client).Reject)
}

// notify tells the peer with publicKey at addr about a decision. The local
// decision already stands; a failed callback is reported but not rolled back.
func (n *Network) notify(ctx context.Context, addr, publicKey string, tell func(*a2a.Client, context.Context, string, string) error) error {
	if addr == "" {
		return nil
	}
	client, err := n.Client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	if err := tell(client, ctx, url(addr), publicKey); err != nil {
		return fmt.Errorf("decided locally, but %s did not answer: %w", addr, err)
	}
	return nil
}

// url is the A2A endpoint of the node at addr (host:port). Nodes are reached
// over TLS only.
func url(addr string) string { return "https://" + addr + "/" }
