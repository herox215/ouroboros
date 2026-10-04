package peers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
)

// patience is how long a test waits for what should happen at once.
const patience = 10 * time.Second

// next is the next message of ch, for a test that must not wait forever.
func next(t *testing.T, ch *a2a.Channel) ([]byte, error) {
	t.Helper()
	type result struct {
		msg []byte
		err error
	}
	got := make(chan result, 1)
	go func() {
		msg, err := ch.Receive()
		got <- result{msg, err}
	}()
	select {
	case r := <-got:
		return r.msg, r.err
	case <-time.After(patience):
		t.Fatal("nothing was received in time")
		return nil, nil
	}
}

// talks gives n the channel "files/watch", as a plugin would: it says who
// opened it and then answers every message in capitals. It returns how often
// the channel was opened.
func talks(n *testNode) *atomic.Int32 {
	ran := new(atomic.Int32)
	n.channels = []a2a.ChannelHandler{{Name: "files/watch", Handle: func(_ context.Context, ch *a2a.Channel) {
		ran.Add(1)
		ch.Send([]byte(n.Name + " watches for " + ch.Peer()))
		for {
			msg, err := ch.Receive()
			if err != nil {
				return
			}
			ch.Send(bytes.ToUpper(msg))
		}
	}}}
	return ran
}

// watch opens "files/watch" on node, which has to answer as name.
func watch(t *testing.T, from *testNode, network *Network, node discovery.Node, name string) *a2a.Channel {
	t.Helper()
	ch, err := network.Open(context.Background(), node, "files/watch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	if msg, err := next(t, ch); err != nil || string(msg) != name+" watches for "+from.PublicKey {
		t.Fatalf("got %q, %v", msg, err)
	}
	return ch
}

// A channel a plugin added to a peer is opened with Open, as this node: the
// peer learns who opened it from the signature, and both talk until one of
// them closes it.
func TestOpenTalksToAPeer(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ran := talks(beta)
	trust(alpha, beta)
	network := alpha.network(beta.Node)

	ch := watch(t, alpha, network, beta.Node, "beta")
	if ch.Peer() != beta.PublicKey {
		t.Fatalf("the peer is %q", ch.Peer())
	}
	for _, msg := range []string{"a.txt", "b.txt"} {
		if err := ch.Send([]byte(msg)); err != nil {
			t.Fatal(err)
		}
		if got, err := next(t, ch); err != nil || string(got) != string(bytes.ToUpper([]byte(msg))) {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	// A connected peer given by its address is found by the key it presents.
	byAddr := watch(t, alpha, network, discovery.Node{Host: beta.Host, Port: beta.Port}, "beta")
	if byAddr.Peer() != beta.PublicKey || ran.Load() != 2 {
		t.Fatalf("by address: the peer is %q, opened %d times", byAddr.Peer(), ran.Load())
	}
	if beta.asked != 0 {
		t.Fatal("a channel is not a message to the agent")
	}
	// A channel the peer has not is its error.
	var refused *a2a.Error
	if _, err := network.Open(context.Background(), beta.Node, "files/unknown"); !errors.As(err, &refused) || refused.Code != a2a.CodeMethodNotFound {
		t.Fatalf("a channel nobody added: %v", err)
	}
}

// Open only goes to peers this node is connected to. To anyone else nothing
// is sent, whether or not the other side would open the channel.
func TestOpenGoesOnlyToConnectedPeers(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ran := talks(beta)
	beta.contacts.AddTrusted(alpha.PublicKey, "alpha", "") // beta would open it
	ctx := context.Background()
	network := alpha.network(beta.Node)

	for what, node := range map[string]discovery.Node{"a stranger": beta.Node, "a stranger by address": {Host: beta.Host, Port: beta.Port}} {
		if ch, err := network.Open(ctx, node, "files/watch"); !errors.Is(err, ErrNotConnected) || ch != nil {
			t.Fatalf("%s: got %v, %v", what, ch, err)
		}
	}
	// The other way round: alpha trusts gamma, but gamma does not know alpha.
	// The request goes out and is refused there, before the handler runs.
	gamma := serve(t, "gamma")
	gammaRan := talks(gamma)
	alpha.contacts.AddTrusted(gamma.PublicKey, "gamma", "")
	var refused *a2a.Error
	if _, err := network.Open(ctx, gamma.Node, "files/watch"); !errors.As(err, &refused) || refused.Code != a2a.CodeUnauthorized {
		t.Fatalf("a peer that is not connected to this node: %v", err)
	}
	if ran.Load() != 0 || gammaRan.Load() != 0 {
		t.Fatalf("the channel was opened: beta %d, gamma %d", ran.Load(), gammaRan.Load())
	}
	if d, _ := gamma.contacts.Snapshot(); len(d.Incoming) != 0 {
		t.Fatal("a refused channel should not leave a connection request")
	}
}

// Open reaches the holder of the key of the peer and nobody else, and what
// decides about trust is not opened by name.
func TestOpenReachesOnlyTheHolderOfTheKey(t *testing.T) {
	alpha, beta, mallory := serve(t, "alpha"), serve(t, "beta"), serve(t, "mallory")
	talks(beta)
	ran := talks(mallory)
	trust(alpha, beta)
	mallory.contacts.AddTrusted(alpha.PublicKey, "alpha", "")
	ctx := context.Background()

	fake := impostor(mallory, beta)
	if _, err := alpha.network(fake).Open(ctx, fake, "files/watch"); !errors.Is(err, a2a.ErrUnverified) || ran.Load() != 0 {
		t.Fatalf("got %v, opened %d times", err, ran.Load())
	}
	network := alpha.network(beta.Node)
	for _, name := range []string{a2a.MethodSend, a2a.MethodAccept, a2a.MethodReject, a2a.MethodOpen} {
		if _, err := network.Open(ctx, beta.Node, name); !errors.Is(err, a2a.ErrBuiltin) {
			t.Errorf("%s: %v", name, err)
		}
	}

	noClient := errors.New("no client")
	network.Client = func() (*a2a.Client, error) { return nil, noClient }
	if _, err := network.Open(ctx, beta.Node, "files/watch"); !errors.Is(err, noClient) {
		t.Errorf("without a client: %v", err)
	}
	// Contacts that cannot be read trust nobody.
	defer breakContacts(t, alpha)()
	if _, err := network.Open(ctx, beta.Node, "files/watch"); err == nil || errors.Is(err, noClient) {
		t.Errorf("without contacts: %v", err)
	}
}

// When the owner decides against a peer, the channels this node opened to it
// are closed at once, and the others are not.
func TestRejectClosesTheChannelsOfThePeer(t *testing.T) {
	alpha, beta, gamma := serve(t, "alpha"), serve(t, "beta"), serve(t, "gamma")
	talks(beta)
	talks(gamma)
	trust(alpha, beta)
	trust(alpha, gamma)
	network := alpha.network(beta.Node, gamma.Node)
	toBeta, toGamma := watch(t, alpha, network, beta.Node, "beta"), watch(t, alpha, network, gamma.Node, "gamma")

	// A list that was changed by hand: beta is a peer and asks to be one.
	data, err := alpha.contacts.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	data.Incoming = []contacts.Request{{PublicKey: beta.PublicKey, Name: "beta"}}
	list, _ := json.Marshal(data)
	if err := os.WriteFile(alpha.contactsPath, list, 0o600); err != nil {
		t.Fatal(err)
	}
	// The request names no address: there is nobody to tell.
	if err := network.Reject(context.Background(), beta.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := next(t, toBeta); !errors.Is(err, a2a.ErrDisconnected) {
		t.Fatalf("the channel to the refused peer: %v", err)
	}
	if err := toGamma.Send([]byte("still here")); err != nil {
		t.Fatal(err)
	}
	if msg, err := next(t, toGamma); err != nil || string(msg) != "STILL HERE" {
		t.Fatalf("the channel to another peer: %q, %v", msg, err)
	}
}

// A node that stops closes the channels it opened, and opens no further one.
func TestCloseClosesTheChannels(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	ran := talks(beta)
	trust(alpha, beta)
	network := alpha.network(beta.Node)
	channels := []*a2a.Channel{watch(t, alpha, network, beta.Node, "beta"), watch(t, alpha, network, beta.Node, "beta")}

	network.Close()
	for _, ch := range channels {
		if _, err := next(t, ch); !errors.Is(err, a2a.ErrStopped) {
			t.Fatalf("got %v", err)
		}
	}
	if ch, err := network.Open(context.Background(), beta.Node, "files/watch"); !errors.Is(err, a2a.ErrStopped) || ch != nil {
		t.Fatalf("opened by a node that stops: %v, %v", ch, err)
	}
	if ran.Load() != 3 {
		t.Fatalf("opened %d times", ran.Load())
	}
}

// A channel that the peer closes ends for this node as well.
func TestPeerClosesTheChannel(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	beta.channels = []a2a.ChannelHandler{{Name: "files/watch", Handle: func(_ context.Context, ch *a2a.Channel) {
		ch.Send([]byte("nothing to watch"))
	}}}
	trust(alpha, beta)
	ch, err := alpha.network(beta.Node).Open(context.Background(), beta.Node, "files/watch")
	if err != nil {
		t.Fatal(err)
	}
	if msg, err := next(t, ch); err != nil || string(msg) != "nothing to watch" {
		t.Fatalf("got %q, %v", msg, err)
	}
	if _, err := next(t, ch); err != io.EOF {
		t.Fatalf("got %v", err)
	}
}
