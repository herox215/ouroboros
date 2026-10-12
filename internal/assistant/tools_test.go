package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
	"ouroboros/internal/identity"
	"ouroboros/internal/peers"
)

var errTest = errors.New("test error")

// testNode is a running node with its own key and contacts.
type testNode struct {
	discovery.Node // how the others find it
	id             *identity.Identity
	contacts       *contacts.Store

	mu     sync.Mutex
	heard  []string // "contextID: text" of every message its agent got
	noted  []string // "contextID: text" of every note it was told
	failed error    // what its agent fails with, if set
	// respond answers instead of "name: text", if set.
	respond a2a.Responder
}

// serveNode runs a node called name whose agent answers "name: text".
func serveNode(t *testing.T, name string) *testNode {
	t.Helper()
	n := &testNode{id: identity.Generate(), contacts: contacts.Open(filepath.Join(t.TempDir(), "contacts.json"))}
	srv := httptest.NewUnstartedServer((&a2a.Server{
		Card:     func() a2a.AgentCard { return a2a.AgentCard{Name: name, Description: "knows the way"} },
		Identity: n.id,
		Trust:    n.contacts,
		Respond: func(ctx context.Context, contextID, text string) (string, error) {
			n.mu.Lock()
			n.heard = append(n.heard, contextID+": "+text)
			respond, failed := n.respond, n.failed
			n.mu.Unlock()
			if respond != nil {
				return respond(ctx, contextID, text)
			}
			return name + ": " + text, failed
		},
		Note: func(_ context.Context, contextID, text string) error {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.noted = append(n.noted, contextID+": "+text)
			return nil
		},
	}).Handler())
	// Like a node: over TLS, with the certificate of its key.
	srv.TLS = a2a.TLSConfig(n.id)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	n.Node = discovery.Node{ID: n.id.ID(), Name: name, Model: "gpt-" + name, PublicKey: n.id.Public(), Host: host}
	n.Port, _ = strconv.Atoi(port)
	return n
}

// messages are the messages the agent of n got so far.
func (n *testNode) messages() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.heard, "\n")
}

// notes are the notes n was told so far.
func (n *testNode) notes() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return strings.Join(n.noted, "\n")
}

// answers makes the agent of n answer with respond from now on.
func (n *testNode) answers(respond a2a.Responder) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.respond = respond
}

// fail makes the agent of n fail with err from now on.
func (n *testNode) fail(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failed = err
}

// finder finds the nodes it holds, or fails.
type finder struct {
	nodes []discovery.Node
	err   error
}

func (f *finder) Find(context.Context) ([]discovery.Node, error) { return f.nodes, f.err }

// self is the node whose tools are tested: it has a key and contacts, and
// finds what found says.
type self struct {
	id       *identity.Identity
	contacts *contacts.Store
	found    *finder
	network  *peers.Network
}

func newSelf(t *testing.T, found ...discovery.Node) *self {
	t.Helper()
	s := &self{id: identity.Generate(), contacts: contacts.Open(filepath.Join(t.TempDir(), "contacts.json")), found: &finder{nodes: found}}
	s.network = &peers.Network{Finder: s.found, Contacts: s.contacts, Client: func() (*a2a.Client, error) {
		return &a2a.Client{Identity: s.id, Name: "me", Port: 4000}, nil
	}}
	return s
}

// node is how the node itself is found on the network.
func (s *self) node() discovery.Node {
	return discovery.Node{ID: s.id.ID(), Name: "me", Model: "gpt-me", PublicKey: s.id.Public(), Host: "127.0.0.1", Port: 4000}
}

// tools are the tools of the node by name.
func (s *self) tools() map[string]agent.Tool {
	tools := map[string]agent.Tool{}
	for _, tool := range PeerTools(s.network, s.id.ID) {
		tools[tool.Name] = tool
	}
	return tools
}

// connect makes s and n trust each other.
func (s *self) connect(t *testing.T, n *testNode) {
	t.Helper()
	if err := errors.Join(s.contacts.AddTrusted(n.PublicKey, n.Name, n.Model), n.contacts.AddTrusted(s.id.Public(), "me", "")); err != nil {
		t.Fatal(err)
	}
}

// ask runs ask_peer of s within the conversation of ctx.
func (s *self) ask(ctx context.Context, peer, message string) (string, error) {
	args, _ := json.Marshal(askPeerArgs{Peer: peer, Message: message})
	return s.tools()[toolAskPeer].Run(ctx, args)
}

// The agent sees who is online and how far the connection to each has come.
// It is not offered itself, nor the peer it is answering.
func TestListPeers(t *testing.T) {
	luna := serveNode(t, "luna")
	mira := discovery.Node{ID: "mira-id", Name: "mira", Model: "gpt-mira", PublicKey: identity.Generate().Public(), Host: "127.0.0.1", Port: 1}
	nova := discovery.Node{ID: "nova-id", Name: "nova", Model: "gpt-nova", PublicKey: identity.Generate().Public(), Host: "127.0.0.1", Port: 2}
	keyless := discovery.Node{ID: "old-id", Name: "old", Model: "gpt-old", Host: "127.0.0.1", Port: 3}
	// Trusted, but nobody answers at its address.
	vega := discovery.Node{ID: "vega-id", Name: "vega", Model: "gpt-vega", PublicKey: identity.Generate().Public(), Host: "127.0.0.1", Port: 4}
	s := newSelf(t)
	s.found.nodes = []discovery.Node{luna.Node, s.node(), mira, nova, keyless, vega}
	s.connect(t, luna)
	if err := errors.Join(s.contacts.AddOutgoing(mira.PublicKey, mira.Name, mira.Model, mira.Addr()),
		s.contacts.AddTrusted(vega.PublicKey, vega.Name, vega.Model)); err != nil {
		t.Fatal(err)
	}
	list := s.tools()[toolListPeers]

	got, err := list.Run(context.Background(), json.RawMessage(`{}`))
	want := "luna (id " + luna.ID + ", model gpt-luna, connected): knows the way\n" +
		"mira (id mira-id, model gpt-mira, connection requested)\n" +
		"nova (id nova-id, model gpt-nova, not connected)\n" +
		"old (id old-id, model gpt-old, not connected)\n" +
		"vega (id vega-id, model gpt-vega, trusted, but not reachable right now)"
	if err != nil || got != want {
		t.Fatalf("for the owner: %v\n got %q\nwant %q", err, got, want)
	}

	// A peer that asks cannot be asked in turn, so it is not listed for itself.
	got, err = list.Run(from(luna.PublicKey), json.RawMessage(`{}`))
	if err != nil || got != want[strings.Index(want, "mira"):] {
		t.Fatalf("for luna: %q, %v", got, err)
	}
	got, err = list.Run(from(nova.PublicKey), json.RawMessage(`{}`))
	if err != nil || strings.Contains(got, "nova") || !strings.Contains(got, "luna") || !strings.Contains(got, "old (") {
		t.Fatalf("for nova: %q, %v", got, err)
	}

	// The trusted peers that are not online are listed all the same.
	offline := "luna (id " + luna.ID + ", trusted, but not reachable right now)\n" +
		"vega (id " + identity.IDOf(vega.PublicKey) + ", trusted, but not reachable right now)"
	s.found.nodes = []discovery.Node{s.node()}
	if got, err := list.Run(context.Background(), json.RawMessage(`{}`)); err != nil || got != offline {
		t.Fatalf("alone: %q, %v", got, err)
	}
	alone := newSelf(t)
	if got, err := alone.tools()[toolListPeers].Run(context.Background(), json.RawMessage(`{}`)); err != nil || got != "No peers are online." {
		t.Fatalf("no peers: %q, %v", got, err)
	}
	s.found.nodes = nil
	if got, err := list.Run(context.Background(), json.RawMessage(`{}`)); err != nil || got != offline {
		t.Fatalf("nobody found: %q, %v", got, err)
	}

	// A network that cannot be searched is not an empty one.
	s.found.err = errTest
	if got, err := list.Run(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, errTest) || got != "" {
		t.Fatalf("failing discovery: %q, %v", got, err)
	}
}

// A connected peer gets the message and its answer comes back, one
// conversation per peer.
func TestAskPeer(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.found.nodes = append(s.found.nodes, s.node())
	s.connect(t, luna)
	ctx := context.Background()

	for _, target := range []string{"luna", luna.ID} {
		if got, err := s.ask(ctx, target, "how are you?"); err != nil || got != "luna: how are you?" {
			t.Fatalf("ask %q: %q, %v", target, got, err)
		}
	}
	// Outside of a conversation the context of the peer is named after the peer alone.
	if got, want := luna.messages(), "/"+luna.ID+": how are you?\n/"+luna.ID+": how are you?"; got != want {
		t.Fatalf("luna got %q, want %q", got, want)
	}

	// Why the agent of a peer failed stays there; that it failed is an error.
	luna.fail(errTest)
	got, err := s.ask(ctx, "luna", "still there?")
	if err == nil || got != "" || !strings.Contains(err.Error(), "the agent could not answer (error ") || strings.Contains(err.Error(), errTest.Error()) {
		t.Fatalf("failing peer: %q, %v", got, err)
	}
}

func TestAskPeerNeedsPeerAndMessage(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	for _, args := range [][2]string{{"", ""}, {"luna", ""}, {"", "hello"}} {
		if got, err := s.ask(context.Background(), args[0], args[1]); err == nil || got != "" || err.Error() != `ask_peer needs "peer" and "message"` {
			t.Errorf("ask_peer(%q, %q) = %q, %v", args[0], args[1], got, err)
		}
	}
	if _, err := s.tools()[toolAskPeer].Run(context.Background(), json.RawMessage(`{"peer": 1}`)); err == nil || err.Error() != `ask_peer: invalid arguments: "peer" is a string, not a number` {
		t.Fatalf("arguments that are no text: %v", err)
	}
	if got := luna.messages(); got != "" {
		t.Fatalf("luna got %q", got)
	}
}

// Nobody is asked who is not a peer: not somebody who is not there, and not
// the node itself.
func TestAskPeerWhoIsNoPeer(t *testing.T) {
	s := newSelf(t)
	s.found.nodes = []discovery.Node{s.node()}
	ctx := context.Background()
	if got, err := s.ask(ctx, "nobody", "hello"); err == nil || got != "" || !strings.Contains(err.Error(), `no node "nobody" found`) {
		t.Fatalf("unknown peer: %q, %v", got, err)
	}
	for _, target := range []string{"me", s.id.ID()} {
		if got, err := s.ask(ctx, target, "hello"); err == nil || got != "" || err.Error() != target+" is this node itself, not a peer" {
			t.Fatalf("ask %q: %q, %v", target, got, err)
		}
	}
	s.found.err = errTest
	if _, err := s.ask(ctx, "luna", "hello"); !errors.Is(err, errTest) {
		t.Fatalf("failing discovery: %v", err)
	}
}

// An agent is not sent to an address: it asks the nodes that are found, by
// their name or ID, and nobody is contacted for anything else.
func TestAskPeerByAddress(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	got, err := s.ask(context.Background(), luna.Addr(), "hello")
	if err == nil || got != "" || !strings.Contains(err.Error(), `no node "`+luna.Addr()+`" found`) {
		t.Fatalf("got %q, %v", got, err)
	}
	if got := luna.messages(); got != "" {
		t.Fatalf("luna got %q", got)
	}
	// By its ID it is the peer it is.
	if got, err := s.ask(context.Background(), luna.ID, "hello"); err != nil || got != "luna: hello" {
		t.Fatalf("by ID: %q, %v", got, err)
	}
	if got, want := luna.messages(), "/"+luna.ID+": hello"; got != want {
		t.Fatalf("luna got %q, want %q", got, want)
	}
}

// A node the owner is not connected to gets nothing of the message, and the
// agent cannot connect to it: it is told to hand the decision to its owner,
// like an answer, for there is nothing wrong with it.
func TestAskPeerThatIsNotConnected(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	ctx := context.Background()
	got, err := s.ask(ctx, "luna", "hello")
	want := "luna (id " + luna.ID + ") is not connected to this node, and nothing was sent to it. " +
		"Only your owner can connect: the node now waits among the connection requests (/pending in the terminal UI) " +
		"for them to confirm. Tell them that, with the ID, so that they can check it is the node they mean."
	if err != nil || got != want {
		t.Fatalf("%v\n got %q\nwant %q", err, got, want)
	}
	theirs, err := luna.contacts.Snapshot()
	if err != nil || len(theirs.Incoming) != 0 {
		t.Fatalf("the agent asked luna to connect: %+v, %v", theirs.Incoming, err)
	}
	ours, err := s.contacts.Snapshot()
	if err != nil || len(ours.Outgoing) != 0 || len(ours.Incoming) != 1 || !ours.Incoming[0].Proposed || ours.Incoming[0].PublicKey != luna.PublicKey {
		t.Fatalf("requests of this node: %+v, %v", ours, err)
	}

	// The owner agrees: luna is asked, and until a human there decides the
	// agent is told to wait.
	if err := s.network.Accept(ctx, luna.PublicKey); err != nil {
		t.Fatal(err)
	}
	got, err = s.ask(ctx, "luna", "hello")
	want = "luna was asked to connect, but nothing of your message was sent: " +
		"a human there has to accept the connection request before luna answers."
	if err != nil || got != want {
		t.Fatalf("%v\n got %q\nwant %q", err, got, want)
	}
	if got := luna.messages(); got != "" {
		t.Fatalf("luna got %q", got)
	}
	theirs, err = luna.contacts.Snapshot()
	if err != nil || len(theirs.Incoming) != 1 || theirs.Incoming[0].PublicKey != s.id.Public() {
		t.Fatalf("requests at luna: %+v, %v", theirs.Incoming, err)
	}

	// A peer that refused is an error, and one that names the peer.
	if _, ok, err := luna.contacts.Reject(s.id.Public()); err != nil || !ok {
		t.Fatalf("reject: %v, %v", ok, err)
	}
	got, err = s.ask(ctx, "luna", "hello")
	if !errors.Is(err, peers.ErrRefused) || got != "" || err.Error() != "luna refused to connect" {
		t.Fatalf("refused: %q, %v", got, err)
	}
}

// The peer that is being answered is not asked in turn: two agents asking
// each other would never finish.
func TestAskPeerThatIsBeingAnswered(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	got, err := s.ask(from(luna.PublicKey), "luna", "hello")
	if !errors.Is(err, peers.ErrSender) || got != "" || !strings.HasPrefix(err.Error(), "luna is the peer you are answering") {
		t.Fatalf("got %q, %v", got, err)
	}
	if got := luna.messages(); got != "" {
		t.Fatalf("luna got %q", got)
	}
	// Another peer may have it asked.
	if got, err := s.ask(from(identity.Generate().Public()), "luna", "hello"); err != nil || got != "luna: hello" {
		t.Fatalf("for another peer: %q, %v", got, err)
	}
}
