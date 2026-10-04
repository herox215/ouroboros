package peers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/discovery"
)

// shares gives n the method "files/share", as a plugin would, and returns
// how often it ran.
func shares(n *testNode) *int {
	ran := new(int)
	n.methods = []a2a.Method{{Name: "files/share", Handle: func(_ context.Context, from a2a.Caller, params json.RawMessage) (*a2a.Message, *a2a.Error) {
		*ran++
		var p struct{ Path string }
		if json.Unmarshal(params, &p) != nil || p.Path == "" {
			return nil, &a2a.Error{Code: a2a.CodeInvalidParams, Message: "needs a path"}
		}
		msg := a2a.NewMessage("agent", "", n.Name+" shares "+p.Path+" with "+from.PublicKey)
		return &msg, nil
	}}}
	return ran
}

// A method a plugin added to a peer is run with Call, as this node: the peer
// learns who calls from the signature.
func TestCallRunsTheMethodOfAPeer(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ran := shares(beta)
	trust(alpha, beta)
	ctx := context.Background()
	network := alpha.network(beta.Node)

	msg, err := network.Call(ctx, beta.Node, "files/share", map[string]string{"path": "a.txt"})
	if err != nil || msg.Text() != "beta shares a.txt with "+alpha.PublicKey || *ran != 1 {
		t.Fatalf("got %+v, %v, ran %d", msg, err, *ran)
	}
	// What the method answers with as an error is the error of Call.
	var failed *a2a.Error
	if _, err := network.Call(ctx, beta.Node, "files/share", nil); !errors.As(err, &failed) || failed.Code != a2a.CodeInvalidParams {
		t.Fatalf("got %v", err)
	}
	// A connected peer given by its address is found by the key it presents.
	byAddr := discovery.Node{Host: beta.Host, Port: beta.Port}
	if msg, err := network.Call(ctx, byAddr, "files/share", map[string]string{"path": "b.txt"}); err != nil || !strings.HasPrefix(msg.Text(), "beta shares b.txt") {
		t.Fatalf("by address: %+v, %v", msg, err)
	}
	if beta.asked != 0 {
		t.Fatal("a call is not a message to the agent")
	}
}

// Call only goes to peers this node is connected to. To anyone else nothing
// is sent, whether or not the other side would answer.
func TestCallGoesOnlyToConnectedPeers(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ran := shares(beta)
	beta.contacts.AddTrusted(alpha.PublicKey, "alpha", "") // beta would answer
	ctx := context.Background()
	network := alpha.network(beta.Node)
	params := map[string]string{"path": "a.txt"}

	notCalled := func(what string, node discovery.Node) {
		t.Helper()
		if msg, err := network.Call(ctx, node, "files/share", params); !errors.Is(err, ErrNotConnected) || msg != nil {
			t.Fatalf("%s: got %+v, %v", what, msg, err)
		}
		if *ran != 0 {
			t.Fatalf("%s: the method ran", what)
		}
	}
	notCalled("a stranger", beta.Node)
	notCalled("a stranger by address", discovery.Node{Host: beta.Host, Port: beta.Port})
	// Asked to connect, but not accepted yet.
	alpha.contacts.AddOutgoing(beta.PublicKey, "beta", "", beta.Addr())
	notCalled("a node that was asked", beta.Node)
	// A node that asked this one, and one that was refused.
	alpha.contacts.RemoveOutgoing(beta.PublicKey)
	alpha.contacts.AddIncoming(beta.PublicKey, "beta", "", beta.Addr())
	notCalled("a node that asks", beta.Node)
	alpha.contacts.Reject(beta.PublicKey)
	notCalled("a refused node", beta.Node)
	if _, err := network.Call(ctx, beta.Node, "files/share", params); !strings.Contains(err.Error(), "beta is not connected to this node") {
		t.Fatalf("the error should name the node: %v", err)
	}

	// The other way round: alpha trusts beta, but beta does not know alpha.
	// The call goes out and is refused there, before the method runs.
	gamma := serve(t, "gamma")
	gammaRan := shares(gamma)
	alpha.contacts.AddTrusted(gamma.PublicKey, "gamma", "")
	var refused *a2a.Error
	if _, err := network.Call(ctx, gamma.Node, "files/share", params); !errors.As(err, &refused) || refused.Code != a2a.CodeUnauthorized || *gammaRan != 0 {
		t.Fatalf("a peer that is not connected to this node: %v, ran %d", err, *gammaRan)
	}
	if d, _ := gamma.contacts.Snapshot(); len(d.Incoming) != 0 {
		t.Fatal("a refused call should not leave a connection request")
	}
}

// Call reaches the holder of the key of the peer and nobody else: a node
// that announces that key at its own address gets nothing, although it has
// the method and would run it for this node.
func TestCallReachesOnlyTheHolderOfTheKey(t *testing.T) {
	alpha, beta, mallory := serve(t, "alpha"), serve(t, "beta"), serve(t, "mallory")
	shares(beta)
	ran := shares(mallory)
	trust(alpha, beta)
	mallory.contacts.AddTrusted(alpha.PublicKey, "alpha", "")
	ctx := context.Background()

	fake := impostor(mallory, beta)
	if _, err := alpha.network(fake).Call(ctx, fake, "files/share", map[string]string{"path": "a.txt"}); !errors.Is(err, a2a.ErrUnverified) || *ran != 0 {
		t.Fatalf("got %v, ran %d", err, *ran)
	}
	// Given by its address, mallory is itself: a node alpha is not connected to.
	byAddr := discovery.Node{Host: mallory.Host, Port: mallory.Port}
	if _, err := alpha.network().Call(ctx, byAddr, "files/share", nil); !errors.Is(err, ErrNotConnected) || *ran != 0 {
		t.Fatalf("by address: %v, ran %d", err, *ran)
	}
}

// What decides about trust is not run by name, and a call that cannot be
// made is an error.
func TestCallErrors(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	trust(alpha, beta)
	ctx := context.Background()
	network := alpha.network(beta.Node)

	for _, method := range []string{a2a.MethodSend, a2a.MethodAccept, a2a.MethodReject} {
		if _, err := network.Call(ctx, beta.Node, method, nil); !errors.Is(err, a2a.ErrBuiltin) {
			t.Errorf("%s: %v", method, err)
		}
	}
	if _, err := network.Call(ctx, discovery.Node{Host: "127.0.0.1", Port: 1}, "files/share", nil); err == nil {
		t.Error("nobody answers at that address")
	}

	noClient := errors.New("no client")
	network.Client = func() (*a2a.Client, error) { return nil, noClient }
	if _, err := network.Call(ctx, beta.Node, "files/share", nil); !errors.Is(err, noClient) {
		t.Errorf("without a client: %v", err)
	}
	// Contacts that cannot be read trust nobody.
	breakContacts(t, alpha)
	if _, err := network.Call(ctx, beta.Node, "files/share", nil); err == nil || errors.Is(err, noClient) {
		t.Errorf("without contacts: %v", err)
	}
}
