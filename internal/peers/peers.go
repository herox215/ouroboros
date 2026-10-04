// Package peers is the other nodes as this node sees them: who is online,
// who is connected, and how to talk to them.
//
// Everything that reaches another node goes through Network: Find and Peers
// list them, Resolve picks one, Send talks to it, Call runs a method a plugin
// added to it, Accept and Reject decide on its connection request. Front ends and tools call these and never use
// discovery, a2a or contacts directly.
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

// Send delivers text to the agent of node within the conversation contextID
// and returns its answer. A node that does not trust this one yet gets a
// connection request instead, reported as ErrPending.
//
// While a peer is being answered (see a2a.Origin), that peer cannot be the
// target: two agents asking each other would never finish.
//
// The message is signed for the public key of node and sent over a connection
// pinned to that key: whoever else answers at its address gets nothing to
// read, and Send fails with a2a.ErrUnverified.
func (n *Network) Send(ctx context.Context, node discovery.Node, contextID, text string) (string, error) {
	if origin, ok := a2a.OriginFrom(ctx); ok {
		// A node without a key cannot be told apart from the sender.
		if node.PublicKey == "" || node.PublicKey == origin.PublicKey {
			return "", ErrSender
		}
	}
	client, err := n.Client()
	if err != nil {
		return "", err
	}
	publicKey := node.PublicKey
	if publicKey == "" {
		// A node given by its address: the key of whoever answers there, and
		// only the holder of that key gets the message.
		if publicKey, err = key(ctx, node); err != nil {
			return "", err
		}
	}
	reply, err := client.Send(ctx, url(node.Addr()), publicKey, contextID, text)
	switch {
	case a2a.IsPending(err):
		// Remembered, so that the acceptance of the peer is recognised later.
		if node.PublicKey != "" {
			if err := n.Contacts.AddOutgoing(node.PublicKey, node.Name, node.Model, node.Addr()); err != nil {
				return "", fmt.Errorf("%w, but it could not be remembered here: %w", ErrPending, err)
			}
		}
		return "", ErrPending
	case a2a.IsBlocked(err):
		return "", ErrRefused
	}
	return reply, err
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
	publicKey := node.PublicKey
	if publicKey == "" {
		// A node given by its address is whoever answers there. It is called
		// only if that is a peer, and only that peer gets the call.
		var err error
		if publicKey, err = key(ctx, node); err != nil {
			return nil, err
		}
	}
	trusted, _, err := n.Contacts.State(publicKey)
	if err != nil {
		return nil, err
	}
	if !trusted {
		return nil, fmt.Errorf("%s %w", Label(node), ErrNotConnected)
	}
	client, err := n.Client()
	if err != nil {
		return nil, err
	}
	return client.Call(ctx, url(node.Addr()), publicKey, method, params)
}

// Accept trusts the peer that sent the connection request and tells it so.
// A decision that could not be stored fails with ErrNotSaved and is not told
// to the peer.
func (n *Network) Accept(ctx context.Context, publicKey string) error {
	req, ok, err := n.Contacts.Approve(publicKey)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSaved, err)
	}
	if !ok {
		return ErrNoRequest
	}
	return n.notify(ctx, req.Addr, publicKey, (*a2a.Client).Accept)
}

// Reject refuses the connection request of a peer and tells it so. Like
// Accept it fails with ErrNotSaved if the decision could not be stored.
func (n *Network) Reject(ctx context.Context, publicKey string) error {
	req, ok, err := n.Contacts.Reject(publicKey)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotSaved, err)
	}
	if !ok {
		return ErrNoRequest
	}
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
