package peers

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/discovery"
)

// lost is a finder that cannot look for nodes.
type lost struct{ err error }

func (l lost) Find(context.Context) ([]discovery.Node, error) { return nil, l.err }

// Without a way to find nodes there are no peers and no names to resolve.
func TestNodesThatCannotBeFound(t *testing.T) {
	offline := errors.New("no network")
	network := serve(t, "alpha").network()
	network.Finder = lost{offline}
	ctx := context.Background()
	if _, err := network.Find(ctx); !errors.Is(err, offline) {
		t.Fatalf("find: %v", err)
	}
	if peers, err := network.Peers(ctx); !errors.Is(err, offline) || peers != nil {
		t.Fatalf("peers: %+v, %v", peers, err)
	}
	if _, err := network.Resolve(ctx, "beta"); !errors.Is(err, offline) {
		t.Fatalf("resolve: %v", err)
	}
}

// Contacts that cannot be read connect nobody: the list of peers fails
// instead of showing everyone as a stranger.
func TestPeersWithoutContacts(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	trust(alpha, beta)
	defer breakContacts(t, alpha)()
	if peers, err := alpha.network(beta.Node).Peers(context.Background()); err == nil || peers != nil {
		t.Fatalf("got %+v, %v", peers, err)
	}
}

// The peer whose message is being answered is not asked in turn, and neither
// is a node that cannot be told apart from it.
func TestSendDoesNotAskTheSender(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	trust(alpha, beta)
	network := alpha.network(beta.Node)
	answering := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: beta.PublicKey})

	for what, node := range map[string]discovery.Node{"the sender": beta.Node, "a node without a key": {Host: beta.Host, Port: beta.Port}} {
		if got, err := network.Send(answering, node, "c", "hi"); !errors.Is(err, ErrSender) || got != "" {
			t.Errorf("%s: %q, %v", what, got, err)
		}
	}
	if beta.asked != 0 {
		t.Fatal("the sender was asked")
	}
	// Another peer is asked, also while a message is answered.
	gamma := serve(t, "gamma")
	trust(alpha, gamma)
	if got, err := network.Send(answering, gamma.Node, "c", "hi"); err != nil || got != "gamma: hi" {
		t.Fatalf("another peer: %q, %v", got, err)
	}
}

// A node that refused this one says so, and a node that cannot sign sends
// nothing.
func TestSendThatIsRefused(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ctx := context.Background()
	beta.contacts.AddIncoming(alpha.PublicKey, "alpha", "", "")
	beta.contacts.Reject(alpha.PublicKey)
	network := alpha.network(beta.Node)
	if got, err := network.Send(ctx, beta.Node, "c", "hi"); !errors.Is(err, ErrRefused) || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}

	noClient := errors.New("no client")
	network.Client = func() (*a2a.Client, error) { return nil, noClient }
	if _, err := network.Send(ctx, beta.Node, "c", "hi"); !errors.Is(err, noClient) {
		t.Fatalf("without a client: %v", err)
	}
}

// A decision stands when it is stored. If the peer cannot be told, that is
// reported, and a peer that left no address is not told at all.
func TestDecisionThatIsNotTold(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ctx := context.Background()
	if _, err := beta.network().Send(ctx, alpha.Node, "c", "hi"); !errors.Is(err, ErrPending) {
		t.Fatalf("first contact: %v", err)
	}
	noClient := errors.New("no client")
	network := alpha.network()
	network.Client = func() (*a2a.Client, error) { return nil, noClient }
	if err := network.Accept(ctx, beta.PublicKey); !errors.Is(err, noClient) {
		t.Fatalf("got %v", err)
	}
	if trusted, _, _ := alpha.contacts.State(beta.PublicKey); !trusted {
		t.Fatal("the decision should stand")
	}
	if trusted, _, _ := beta.contacts.State(alpha.PublicKey); trusted || !hadOutgoing(beta, alpha.PublicKey) {
		t.Fatal("beta heard of a decision nobody told it")
	}

	// A request without an address: nothing is sent, so no client is needed.
	alpha.contacts.AddIncoming("pk-gamma", "gamma", "", "")
	if err := network.Accept(ctx, "pk-gamma"); err != nil {
		t.Fatal(err)
	}
	if trusted, _, _ := alpha.contacts.State("pk-gamma"); !trusted {
		t.Fatal("gamma should be trusted")
	}
	// And what nobody asked for cannot be accepted.
	if err := network.Accept(ctx, "pk-nobody"); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("no request: %v", err)
	}
}
