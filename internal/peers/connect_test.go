package peers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
	"ouroboros/internal/identity"
)

// proposals returns the nodes the agent of n proposed to its owner.
func proposals(t *testing.T, n *testNode) []contacts.Request {
	t.Helper()
	d, err := n.contacts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var proposed []contacts.Request
	for _, r := range d.Incoming {
		if r.Proposed {
			proposed = append(proposed, r)
		}
	}
	return proposed
}

// What the owner has to say reaches a node only once both owners agreed to
// connect: a stranger is not even contacted before the owner of this node
// decided, and gets a request without the message then.
func TestSendTellsAStrangerNothing(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	network := alice.network(bob.Node)
	ctx := context.Background()

	for range 2 {
		if got, err := network.Send(ctx, bob.Node, "c", "secret"); !errors.Is(err, ErrUnconfirmed) || got != "" || !strings.HasPrefix(err.Error(), "bob is not connected") {
			t.Fatalf("a stranger: %q, %v", got, err)
		}
	}
	if bob.hits != 0 {
		t.Fatalf("bob was contacted %d times before the owner of alice decided", bob.hits)
	}
	proposed := proposals(t, alice)
	if len(proposed) != 1 || proposed[0].PublicKey != bob.PublicKey || proposed[0].Name != "bob" || proposed[0].Addr != bob.Addr() {
		t.Fatalf("proposed to the owner: %+v", proposed)
	}
	if hadOutgoing(alice, bob.PublicKey) {
		t.Fatal("the agent asked bob to connect by itself")
	}

	// The owner agrees: now bob is asked, and its owner decides.
	if err := network.Accept(ctx, bob.PublicKey); err != nil {
		t.Fatal(err)
	}
	theirs, _ := bob.contacts.Snapshot()
	if len(theirs.Incoming) != 1 || theirs.Incoming[0].PublicKey != alice.PublicKey || theirs.Incoming[0].Proposed {
		t.Fatalf("requests at bob: %+v", theirs.Incoming)
	}
	if trusted, _, _ := alice.contacts.State(bob.PublicKey); trusted || !hadOutgoing(alice, bob.PublicKey) || len(proposals(t, alice)) != 0 {
		t.Fatal("alice should wait for bob, and not trust it yet")
	}
	if got, err := network.Send(ctx, bob.Node, "c", "secret"); !errors.Is(err, ErrPending) || got != "" {
		t.Fatalf("while bob has not decided: %q, %v", got, err)
	}
	if bob.asked != 0 {
		t.Fatal("bob got to read a message before it was connected")
	}

	if err := bob.network().Accept(ctx, alice.PublicKey); err != nil {
		t.Fatal(err)
	}
	if got, err := network.Send(ctx, bob.Node, "c", "secret"); err != nil || got != "bob: secret" {
		t.Fatalf("connected: %q, %v", got, err)
	}
}

// A node given by its address is whoever answers there: a stranger like any
// other, proposed with the key it holds.
func TestSendToTheAddressOfAStranger(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	_, err := alice.network().Send(context.Background(), discovery.Node{Host: bob.Host, Port: bob.Port}, "c", "secret")
	if !errors.Is(err, ErrUnconfirmed) || !strings.HasPrefix(err.Error(), bob.Addr()+" is not connected") {
		t.Fatalf("got %v", err)
	}
	proposed := proposals(t, alice)
	if len(proposed) != 1 || proposed[0].PublicKey != bob.PublicKey || proposed[0].Addr != bob.Addr() {
		t.Fatalf("proposed to the owner: %+v", proposed)
	}
	if bob.asked != 0 {
		t.Fatal("bob got to read the message")
	}
}

// The node itself is no stranger: its owner writes to its own agent.
func TestSendToTheNodeItself(t *testing.T) {
	alice := serve(t, "alice")
	if got, err := alice.network().Send(context.Background(), alice.Node, "c", "hi"); err != nil || got != "alice: hi" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A peer whose acceptance never arrived says that it is connected when it is
// asked again, and is trusted then: by a message, or when the owner agrees to
// a proposal.
func TestPeerThatAcceptedUnheard(t *testing.T) {
	ctx := context.Background()
	t.Run("asked again by a message", func(t *testing.T) {
		alice, bob := serve(t, "alice"), serve(t, "bob")
		request(t, alice, bob)
		bob.contacts.Approve(alice.PublicKey) // and alice is not told
		if got, err := alice.network().Send(ctx, bob.Node, "c", "hi"); err != nil || got != "bob: hi" {
			t.Fatalf("got %q, %v", got, err)
		}
		if trusted, _, _ := alice.contacts.State(bob.PublicKey); !trusted || hadOutgoing(alice, bob.PublicKey) {
			t.Fatal("alice should trust bob and wait for nothing")
		}
	})
	t.Run("asked by the owner", func(t *testing.T) {
		alice, bob := serve(t, "alice"), serve(t, "bob")
		bob.contacts.AddTrusted(alice.PublicKey, "alice", "")
		request(t, alice, bob)
		if trusted, _, _ := alice.contacts.State(bob.PublicKey); !trusted {
			t.Fatal("alice should trust bob")
		}
	})
	t.Run("and it cannot be stored", func(t *testing.T) {
		alice, bob := serve(t, "alice"), serve(t, "bob")
		request(t, alice, bob)
		bob.contacts.Approve(alice.PublicKey)
		bob.before = func() { t.Cleanup(breakContacts(t, alice)) }
		if got, err := alice.network().Send(ctx, bob.Node, "c", "hi"); err == nil || got != "" {
			t.Fatalf("got %q, %v", got, err)
		}
		if bob.asked != 0 {
			t.Fatal("bob got a message from a node that does not trust it")
		}
	})
}

// A proposal the owner declines is dropped: the node was never contacted, is
// not told and not blocked, so its own request would still be heard.
func TestProposalThatIsDeclined(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	ctx := context.Background()
	network := alice.network()
	if _, err := network.Send(ctx, bob.Node, "c", "hi"); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err := network.Reject(ctx, bob.PublicKey); err != nil {
		t.Fatal(err)
	}
	d, _ := alice.contacts.Snapshot()
	if len(d.Incoming) != 0 || len(d.Outgoing) != 0 || len(d.Blocked) != 0 || bob.hits != 0 {
		t.Fatalf("contacts %+v, bob was contacted %d times", d, bob.hits)
	}
}

// Nobody but the owner makes the node ask for a connection: not a peer whose
// message is being answered, and nothing asks a node the owner refused.
func TestSendProposesOnlyForTheOwner(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	network := alice.network()
	answering := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: identity.Generate().Public()})
	if got, err := network.Send(answering, bob.Node, "c", "hi"); !errors.Is(err, ErrNotConnected) || got != "" {
		t.Fatalf("for a peer: %q, %v", got, err)
	}
	// Not even a node the owner asked already is asked again for a peer.
	alice.contacts.AddOutgoing(bob.PublicKey, "bob", "", "")
	if _, err := network.Send(answering, bob.Node, "c", "hi"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("for a peer, a node that was asked: %v", err)
	}
	alice.contacts.RemoveOutgoing(bob.PublicKey)

	alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
	alice.contacts.Reject(bob.PublicKey)
	if got, err := network.Send(context.Background(), bob.Node, "c", "hi"); !errors.Is(err, ErrNotConnected) || got != "" {
		t.Fatalf("a node the owner refused: %q, %v", got, err)
	}
	if len(proposals(t, alice)) != 0 || bob.hits != 0 {
		t.Fatalf("proposed %+v, bob was contacted %d times", proposals(t, alice), bob.hits)
	}
}

// What stands in the way of a connection is reported, and nothing is sent.
func TestSendThatCannotConnect(t *testing.T) {
	ctx := context.Background()
	t.Run("too many requests are waiting", func(t *testing.T) {
		alice, bob := serve(t, "alice"), serve(t, "bob")
		for range contacts.MaxIncoming {
			alice.contacts.AddIncoming(identity.Generate().Public(), "someone", "", "")
		}
		if _, err := alice.network().Send(ctx, bob.Node, "c", "hi"); !errors.Is(err, contacts.ErrFull) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("contacts that cannot be read", func(t *testing.T) {
		alice, bob := serve(t, "alice"), serve(t, "bob")
		trust(alice, bob)
		defer breakContacts(t, alice)()
		if _, err := alice.network().Send(ctx, bob.Node, "c", "hi"); err == nil || errors.Is(err, ErrUnconfirmed) {
			t.Fatalf("got %v", err)
		}
		if bob.hits != 0 {
			t.Fatal("bob was contacted without knowing whether it is a peer")
		}
	})
	t.Run("a node that is gone", func(t *testing.T) {
		alice, bob := serve(t, "alice"), serve(t, "bob")
		alice.contacts.AddOutgoing(bob.PublicKey, "bob", "", "")
		bob.srv.Close()
		_, err := alice.network().Send(ctx, bob.Node, "c", "hi")
		if err == nil || errors.Is(err, ErrPending) || errors.Is(err, ErrRefused) {
			t.Fatalf("got %v", err)
		}
		if !hadOutgoing(alice, bob.PublicKey) {
			t.Fatal("the request should still wait for bob")
		}
	})
}

// The owner's decision to ask a node stands when it is stored. If the node
// cannot be asked, that is reported.
func TestProposalThatIsNotDelivered(t *testing.T) {
	ctx := context.Background()
	propose := func(t *testing.T) (alice, bob *testNode) {
		alice, bob = serve(t, "alice"), serve(t, "bob")
		if _, err := alice.network().Send(ctx, bob.Node, "c", "hi"); !errors.Is(err, ErrUnconfirmed) {
			t.Fatal(err)
		}
		return alice, bob
	}
	t.Run("without a client", func(t *testing.T) {
		alice, bob := propose(t)
		noClient := errors.New("no client")
		network := alice.network()
		network.Client = func() (*a2a.Client, error) { return nil, noClient }
		if err := network.Accept(ctx, bob.PublicKey); !errors.Is(err, noClient) {
			t.Fatalf("got %v", err)
		}
		if !hadOutgoing(alice, bob.PublicKey) {
			t.Fatal("the decision should stand")
		}
	})
	t.Run("a node that is gone", func(t *testing.T) {
		alice, bob := propose(t)
		bob.srv.Close()
		err := alice.network().Accept(ctx, bob.PublicKey)
		if err == nil || !strings.Contains(err.Error(), "decided locally, but "+bob.Addr()+" was not asked") {
			t.Fatalf("got %v", err)
		}
		if !hadOutgoing(alice, bob.PublicKey) {
			t.Fatal("the decision should stand")
		}
	})
	t.Run("a node that refuses", func(t *testing.T) {
		alice, bob := propose(t)
		bob.contacts.AddIncoming(alice.PublicKey, "alice", "", "")
		bob.contacts.Reject(alice.PublicKey)
		if err := alice.network().Accept(ctx, bob.PublicKey); !errors.Is(err, ErrRefused) {
			t.Fatalf("got %v", err)
		}
		if hadOutgoing(alice, bob.PublicKey) {
			t.Fatal("a request that was refused is still waiting")
		}
	})
}

// An agent is not sent to an address: Online knows the nodes that are found.
func TestOnlineTakesNoAddress(t *testing.T) {
	network := &Network{Finder: finder{{ID: "abc123", Name: "alpha"}}}
	if n, err := network.Online(context.Background(), "abc"); err != nil || n.Name != "alpha" {
		t.Fatalf("by ID prefix: %+v, %v", n, err)
	}
	if n, err := network.Online(context.Background(), "10.0.0.7:4000"); err == nil {
		t.Fatalf("an address: %+v", n)
	}
}

// A trusted peer that is not online is told as such, not as a node nobody
// knows; one that cannot be told by what names it is not.
func TestOnlineTellsATrustedPeerThatIsNot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := contacts.Open(filepath.Join(dir, "contacts.json"))
	luna, lyra := identity.Generate(), identity.Generate()
	if err := errors.Join(store.AddTrusted(luna.Public(), "luna", ""), store.AddTrusted(lyra.Public(), "", "")); err != nil {
		t.Fatal(err)
	}
	network := &Network{Finder: finder{{ID: "abc123", Name: "alpha"}}, Contacts: store}
	for target, want := range map[string]string{
		"luna":        "luna is a peer you trust, but its node is not online right now: try it again later",
		luna.ID()[:6]: "luna is a peer you trust, but its node is not online right now: try it again later",
		lyra.ID():     lyra.ID() + " is a peer you trust, but its node is not online right now: try it again later",
		"nobody":      `no node "nobody" found (see: ouro list)`,
	} {
		if _, err := network.Online(ctx, target); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v, want %q", target, err, want)
		}
	}
	// What names more than one trusted peer names none.
	if _, err := (&Network{Finder: finder{}, Contacts: store}).Online(ctx, ""); err == nil || err.Error() != `no node "" found (see: ouro list)` {
		t.Errorf("two peers: %v", err)
	}
	// Contacts that cannot be read know no peer.
	os.WriteFile(filepath.Join(dir, "contacts.json"), []byte("{"), 0o600)
	if _, err := network.Online(ctx, "luna"); err == nil || !strings.HasPrefix(err.Error(), `no node "luna" found`) {
		t.Errorf("contacts that cannot be read: %v", err)
	}
}

// A peer of this node that does not know it any more is told the message as
// a peer, takes it for a connection request, and its acceptance is waited for.
func TestPeerThatForgotThisNode(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	alice.contacts.AddTrusted(bob.PublicKey, "bob", "")
	if got, err := alice.network().Send(context.Background(), bob.Node, "c", "hi"); !errors.Is(err, ErrPending) || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !hadOutgoing(alice, bob.PublicKey) || bob.asked != 0 {
		t.Fatal("alice should wait for bob, whose agent should not have answered")
	}
}
