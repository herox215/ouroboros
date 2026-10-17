package peers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
	"ouroboros/internal/identity"
)

// finder finds the nodes it consists of.
type finder []discovery.Node

func (f finder) Find(context.Context) ([]discovery.Node, error) { return f, nil }

func TestMatch(t *testing.T) {
	nodes := []discovery.Node{{ID: "abc123", Name: "alpha"}, {ID: "abd456", Name: "beta"}, {ID: "ab", Name: "gamma"}}
	tests := []struct {
		target, want string
		ok           bool
	}{
		{"abc123", "abc123", true}, // full ID
		{"abc", "abc123", true},    // unique prefix
		{"beta", "abd456", true},   // name
		{"ab", "ab", true},         // exact ID wins over prefix matches
		{"a", "", false},           // ambiguous
		{"zzz", "", false},         // unknown
	}
	for _, tt := range tests {
		n, err := match(nodes, tt.target)
		if (err == nil) != tt.ok || n.ID != tt.want {
			t.Errorf("match(%q) = %+v, %v", tt.target, n, err)
		}
	}
}

func TestResolve(t *testing.T) {
	network := &Network{Finder: finder{{ID: "abc123", Name: "alpha"}}}
	ctx := context.Background()
	if n, err := network.Resolve(ctx, "alpha"); err != nil || n.ID != "abc123" {
		t.Fatalf("by name: %+v, %v", n, err)
	}
	// An address needs nobody to be found.
	if n, err := network.Resolve(ctx, "10.0.0.7:4000"); err != nil || n.Addr() != "10.0.0.7:4000" {
		t.Fatalf("by address: %+v, %v", n, err)
	}
	for _, target := range []string{"10.0.0.7:http", "a:b:c", "nobody"} {
		if _, err := network.Resolve(ctx, target); err == nil {
			t.Errorf("%q: want error", target)
		}
	}
}

// testNode is a running node with its own key and contacts.
type testNode struct {
	discovery.Node // how the others find it
	id             *identity.Identity
	contacts       *contacts.Store
	contactsPath   string
	srv            *httptest.Server
	asked          int    // messages its agent answered
	hits           int    // requests that reached it, of whatever kind
	before         func() // runs before it handles a request, if set
	methods        []a2a.Method
	channels       []a2a.ChannelHandler
	// respond answers instead of "name: text", if set, and notes are what
	// the node was told without being asked.
	respond func(ctx context.Context, text string) (string, error)
	notes   []string
}

// serve runs a node called name whose agent answers "name: text".
func serve(t *testing.T, name string) *testNode {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.json")
	n := &testNode{id: identity.Generate(), contacts: contacts.Open(path), contactsPath: path}
	server := &a2a.Server{
		Card:     func() a2a.AgentCard { return a2a.AgentCard{Name: name, Description: "  knows\nthe way "} },
		Identity: n.id,
		Trust:    n.contacts,
		Respond: func(ctx context.Context, _, text string) (string, error) {
			n.asked++
			if n.respond != nil {
				return n.respond(ctx, text)
			}
			return name + ": " + text, nil
		},
		Note: func(_ context.Context, contextID, text string) error {
			n.notes = append(n.notes, contextID+": "+text)
			return nil
		},
		Methods:  func() []a2a.Method { return n.methods },
		Channels: func() []a2a.ChannelHandler { return n.channels },
	}
	handler := server.Handler()
	n.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.hits++
		if n.before != nil {
			n.before()
		}
		handler.ServeHTTP(w, r)
	}))
	// Like a node: over TLS, with the certificate of its key.
	n.srv.TLS = a2a.TLSConfig(n.id)
	n.srv.StartTLS()
	t.Cleanup(n.srv.Close)
	t.Cleanup(server.Close) // first: its channels do not end by themselves
	host, port, _ := net.SplitHostPort(n.srv.Listener.Addr().String())
	n.Node = discovery.Node{ID: name, Name: name, PublicKey: n.id.Public(), Host: host}
	n.Port, _ = strconv.Atoi(port)
	return n
}

// network is how n reaches the nodes found.
func (n *testNode) network(found ...discovery.Node) *Network {
	return &Network{Finder: finder(found), Contacts: n.contacts, Client: func() (*a2a.Client, error) {
		return &a2a.Client{Identity: n.id, Name: n.Name, Port: n.Port}, nil
	}}
}

// request makes a ask b to connect, the way it takes: the agent of a proposes
// b, and the owner of a agrees.
func request(t *testing.T, a, b *testNode) {
	t.Helper()
	ctx := context.Background()
	if _, err := a.network().Send(ctx, b.Node, "c", "hi"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("first contact: %v", err)
	}
	if err := a.network().Accept(ctx, b.PublicKey); err != nil {
		t.Fatalf("the owner agrees: %v", err)
	}
}

// trust connects the nodes a and b.
func trust(a, b *testNode) {
	a.contacts.AddTrusted(b.PublicKey, b.Name, "")
	b.contacts.AddTrusted(a.PublicKey, a.Name, "")
}

func TestDescribe(t *testing.T) {
	alpha := serve(t, "alpha")
	ctx := context.Background()
	if got, ok := describe(ctx, alpha.Node); !ok || got != "knows the way" {
		t.Fatalf("describe = %q, %v", got, ok)
	}
	other := alpha.Node
	other.PublicKey = identity.Generate().Public()
	if got, ok := describe(ctx, other); ok || got != "" {
		t.Fatalf("a node that holds another key is not asked: %q, %v", got, ok)
	}
	alpha.srv.Close()
	if got, ok := describe(ctx, alpha.Node); ok || got != "" {
		t.Fatalf("a node that does not answer has no description: %q, %v", got, ok)
	}
}

// impostor is mallory as it announces itself: with the ID, name and public
// key of victim, at its own address.
func impostor(mallory, victim *testNode) discovery.Node {
	node := victim.Node
	node.Host, node.Port = mallory.Host, mallory.Port
	return node
}

// Announcing the key of a peer the owner trusts connects nobody: connected is
// the node that proves to hold the key in the TLS handshake. A trusted peer
// that does not is unreachable, which asks the owner for nothing.
func TestPeersConnectsOnlyTheHolderOfTheKey(t *testing.T) {
	alice, bob, carol, mallory := serve(t, "alice"), serve(t, "bob"), serve(t, "carol"), serve(t, "mallory")
	trust(alice, bob)
	alice.contacts.AddOutgoing(carol.PublicKey, "carol", "", "")
	// A trusted peer whose address answers without TLS, as a node used to.
	dave := discovery.Node{ID: "dave", Name: "dave", PublicKey: identity.Generate().Public()}
	alice.contacts.AddTrusted(dave.PublicKey, "dave", "")
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a node without TLS was asked")
	}))
	defer open.Close()
	host, port, _ := net.SplitHostPort(open.Listener.Addr().String())
	dave.Host = host
	dave.Port, _ = strconv.Atoi(port)

	got, err := alice.network(bob.Node, impostor(mallory, bob), carol.Node, mallory.Node, dave).Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		state State
		about string
	}{{Connected, "knows the way"}, {Unreachable, ""}, {Requested, ""}, {NotConnected, ""}, {Unreachable, ""}}
	for i, w := range want {
		if got[i].State != w.state || got[i].About != w.about {
			t.Errorf("peer %d: %v %q, want %v %q", i, got[i].State, got[i].About, w.state, w.about)
		}
	}

	// A peer that is trusted and holds its key, but is gone, is unreachable.
	// A trusted peer that is not online follows, known by its contact.
	bob.srv.Close()
	got, err = alice.network(bob.Node).Peers(context.Background())
	if err != nil || len(got) != 2 || got[0].PublicKey != bob.PublicKey || got[0].State != Unreachable {
		t.Fatalf("a node that is gone: %+v, %v", got, err)
	}
	if offline := got[1]; offline.PublicKey != dave.PublicKey || offline.Name != "dave" || offline.ID != identity.IDOf(dave.PublicKey) || offline.State != Unreachable {
		t.Errorf("a trusted peer that is not online: %+v", offline)
	}
}

// A message goes to the node it is meant for, and only that node's answer
// counts.
func TestSendAcceptsOnlyTheAnswerOfTheNode(t *testing.T) {
	alice, bob, mallory := serve(t, "alice"), serve(t, "bob"), serve(t, "mallory")
	trust(alice, bob)
	trust(alice, mallory)
	network := alice.network()
	ctx := context.Background()

	if got, err := network.Send(ctx, bob.Node, "c", "hi"); err != nil || got != "bob: hi" {
		t.Fatalf("bob: %q, %v", got, err)
	}
	// Mallory trusts alice too, but does not hold bob's key: the handshake
	// fails and it never sees what alice wrote to bob.
	if got, err := network.Send(ctx, impostor(mallory, bob), "c", "hi"); !errors.Is(err, a2a.ErrUnverified) || got != "" {
		t.Fatalf("impostor: %q, %v", got, err)
	}
	if mallory.asked != 0 {
		t.Fatal("the impostor got to read a message for bob")
	}

	// A node given by its address is whoever answers there.
	if got, err := network.Send(ctx, discovery.Node{Host: bob.Host, Port: bob.Port}, "c", "hi"); err != nil || got != "bob: hi" {
		t.Fatalf("by address: %q, %v", got, err)
	}
	bob.srv.Close()
	if _, err := network.Send(ctx, discovery.Node{Host: bob.Host, Port: bob.Port}, "c", "hi"); err == nil {
		t.Fatal("a node that is gone has no key")
	}
}

// The decision on a request is told to the node that asked, nobody else.
func TestDecisionsReachOnlyTheNodeThatAsked(t *testing.T) {
	alice, bob, mallory := serve(t, "alice"), serve(t, "bob"), serve(t, "mallory")
	ctx := context.Background()

	request(t, bob, alice)
	if err := alice.network().Accept(ctx, bob.PublicKey); err != nil {
		t.Fatal(err)
	}
	if trusted, _, _ := bob.contacts.State(alice.PublicKey); !trusted {
		t.Fatal("bob should have heard that alice accepted")
	}

	// A request that claims to come from where mallory listens.
	alice.contacts.AddIncoming(identity.Generate().Public(), "carol", "", mallory.Addr())
	requests, _ := alice.contacts.Snapshot()
	err := alice.network().Reject(ctx, requests.Incoming[0].PublicKey)
	if !errors.Is(err, a2a.ErrUnverified) {
		t.Fatalf("a decision answered by another node: %v", err)
	}
	if err := alice.network().Reject(ctx, "nobody"); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("no request: %v", err)
	}
}

// A card somebody else wrote is cut to size.
func TestOneLine(t *testing.T) {
	// One that just fits stays whole.
	if fits := strings.Repeat("ä", maxCard); oneLine(fits) != fits {
		t.Fatalf("a card of %d characters was cut", maxCard)
	}
	got := oneLine(strings.Repeat("ä", maxCard+50))
	if utf8.RuneCountInString(got) != maxCard || !strings.HasSuffix(got, "ä…") {
		t.Fatalf("long card: %d characters", utf8.RuneCountInString(got))
	}
}

func TestStateAndLabel(t *testing.T) {
	for state, want := range map[State]string{NotConnected: "not connected", Requested: "connection requested", Connected: "connected",
		Unreachable: "trusted, but not reachable right now"} {
		if state.String() != want {
			t.Errorf("%d = %q", state, state)
		}
	}
	if got := Label(discovery.Node{Host: "h", Port: 1}); got != "h:1" {
		t.Fatalf("a node without a name is called by its address: %q", got)
	}
}

// breakContacts makes every read and change of the contacts of n fail until
// the returned function is called: a directory is in the place of their lock.
func breakContacts(t *testing.T, n *testNode) (repair func()) {
	t.Helper()
	lock := n.contactsPath + ".lock"
	os.Remove(lock)
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	return func() { os.Remove(lock) }
}

// A decision that cannot be stored is not made and not told to the peer: the
// request stays, to be decided again.
func TestDecisionThatIsNotSaved(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	ctx := context.Background()
	request(t, bob, alice)

	repair := breakContacts(t, alice)
	for name, decide := range map[string]func(context.Context, string) error{"accept": alice.network().Accept, "reject": alice.network().Reject} {
		if err := decide(ctx, bob.PublicKey); !errors.Is(err, ErrNotSaved) || errors.Is(err, ErrNoRequest) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	repair()
	if d, _ := alice.contacts.Snapshot(); len(d.Incoming) != 1 || len(d.Trusted) != 0 || len(d.Blocked) != 0 {
		t.Fatalf("a decision that failed changed the contacts: %+v", d)
	}
	if trusted, _, _ := bob.contacts.State(alice.PublicKey); trusted || !hadOutgoing(bob, alice.PublicKey) {
		t.Fatal("bob heard of a decision that was not made")
	}

	// The second try works.
	if err := alice.network().Accept(ctx, bob.PublicKey); err != nil {
		t.Fatal(err)
	}
	if trusted, _, _ := bob.contacts.State(alice.PublicKey); !trusted {
		t.Fatal("bob should have heard that alice accepted")
	}
}

// A request that this node cannot remember is reported: the acceptance of
// the peer would not be recognised.
func TestRequestThatIsNotRemembered(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	// Bob trusts alice, who does not know bob any more.
	bob.contacts.AddTrusted(alice.PublicKey, "alice", "")
	alice.before = func() { t.Cleanup(breakContacts(t, bob)) }
	_, err := bob.network().Send(context.Background(), alice.Node, "c", "hi")
	if !errors.Is(err, ErrPending) || !strings.Contains(err.Error(), "could not be remembered") {
		t.Fatalf("got %v", err)
	}
}

// hadOutgoing reports whether n asked publicKey to connect.
func hadOutgoing(n *testNode, publicKey string) bool {
	asked, _ := n.contacts.HadOutgoing(publicKey)
	return asked
}

// A peer an agent names by its ID, or the start of it, is called by the name
// it is trusted as.
func TestNameOf(t *testing.T) {
	alice, bob, carol := serve(t, "alice"), serve(t, "bob"), serve(t, "carol")
	n := alice.network()
	alice.contacts.AddTrusted(bob.PublicKey, "bob", "")
	alice.contacts.AddTrusted(carol.PublicKey, "carol", "")
	bobID, carolID := identity.IDOf(bob.PublicKey), identity.IDOf(carol.PublicKey)
	// A start of the ID that fits only bob's, and one that fits both, if there is one.
	common := 0
	for common < len(bobID) && bobID[common] == carolID[common] {
		common++
	}
	for target, want := range map[string]string{
		bobID:                          "bob",
		bobID[:common+1]:               "bob",
		carolID:                        "carol",
		bobID[:common]:                 "", // carol's too, or every ID
		"bob":                          "", // a name is not an ID
		identity.IDOf(alice.PublicKey): "",
		"ffffffffffffff":               "",
	} {
		if got := n.NameOf(target); got != want {
			t.Errorf("NameOf(%q) = %q, want %q", target, got, want)
		}
	}
	// An empty target is the start of every ID, even where only one peer is trusted.
	bob.contacts.AddTrusted(alice.PublicKey, "alice", "")
	if got := bob.network().NameOf(""); got != "" {
		t.Errorf("NameOf(\"\") = %q with one trusted peer", got)
	}
	os.Remove(alice.contactsPath)
	os.Mkdir(alice.contactsPath, 0o700)
	if got := n.NameOf(bobID); got != "" {
		t.Errorf("contacts that cannot be read: NameOf = %q", got)
	}
}
