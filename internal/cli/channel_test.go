package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/plugin"
)

// patience is how long a test waits for what should happen at once.
const patience = 10 * time.Second

// flow is a plugin that adds channels to its node:
//
//	flow/talk  greets whoever opens it and answers every message in capitals
//	flow/sum   takes pieces until an empty one and answers with their checksum
//	flow/back  opens flow/talk on the peer that opened it, and passes on its greeting
//
// It records why each flow/talk ended.
type flow struct {
	opened  atomic.Int32 // how often flow/talk was opened
	builtin atomic.Int32 // how often its channel with a built-in name was opened
	ended   chan error
}

func newFlow() *flow { return &flow{ended: make(chan error, 16)} }

func (f *flow) plugin() plugin.Plugin {
	type needs struct {
		Network *plugin.Network
	}
	return plugin.New("flow", func(ctx *plugin.Context, n needs) error {
		plugin.Add(ctx, plugin.ChannelHandler{Name: "flow/talk", Handle: func(_ context.Context, ch *plugin.Channel) {
			f.opened.Add(1)
			ch.Send([]byte("hello " + ch.Peer()))
			for {
				msg, err := ch.Receive()
				if err != nil {
					f.ended <- err
					return
				}
				ch.Send(bytes.ToUpper(msg))
			}
		}})
		plugin.Add(ctx, plugin.ChannelHandler{Name: "flow/sum", Handle: func(_ context.Context, ch *plugin.Channel) {
			sum := sha256.New()
			for {
				piece, err := ch.Receive()
				if err != nil {
					return
				}
				if len(piece) == 0 {
					ch.Send(sum.Sum(nil))
					return
				}
				sum.Write(piece)
			}
		}})
		plugin.Add(ctx, plugin.ChannelHandler{Name: "flow/back", Handle: func(c context.Context, ch *plugin.Channel) {
			nodes, _ := n.Network.Find(c)
			for _, node := range nodes {
				if node.PublicKey != ch.Peer() {
					continue
				}
				// Not closed here, and not with this channel: it is the
				// node's to close when it stops.
				back, err := n.Network.Open(context.Background(), node, "flow/talk")
				if err != nil {
					ch.CloseWith(err.Error())
					return
				}
				greeting, _ := back.Receive()
				ch.Send(greeting)
				<-c.Done()
				return
			}
			ch.CloseWith("who are you?")
		}})
		// A plugin that tries to take the place of a built-in method.
		plugin.Add(ctx, plugin.ChannelHandler{Name: a2a.MethodSend, Handle: func(context.Context, *plugin.Channel) {
			f.builtin.Add(1)
		}})
		return nil
	})
}

// why is why a flow/talk of the plugin ended.
func (f *flow) why(t *testing.T) error {
	t.Helper()
	select {
	case err := <-f.ended:
		return err
	case <-time.After(patience):
		t.Fatal("the channel did not end")
		return nil
	}
}

// flowNode starts a node called name with the flow plugin and returns what
// stops it; the test does at its end.
func flowNode(t *testing.T, name string) (s cli.Settings, f *flow, stop func()) {
	t.Helper()
	s, f = clitest.Settings(t), newFlow()
	cfg := clitest.NodeConfig(s)
	cfg.Name = name
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	app, _ := clitest.NewApp(t, s, "", f.plugin())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, []string{"serve"}) }()
	stopped := false
	stop = func() {
		if cancel(); !stopped {
			stopped = true
			if err := <-done; err != nil {
				t.Errorf("serve: %v", err)
			}
		}
	}
	t.Cleanup(stop)
	if _, ok := daemon.WaitRunning(filepath.Join(s.Home, "ouro.pid"), clitest.Patience); !ok {
		t.Fatal("node did not start")
	}
	return s, f, stop
}

// connect makes the owners of the nodes of a and b trust each other, and the
// nodes find each other.
func connect(t *testing.T, a, b cli.Settings) {
	t.Helper()
	clitest.Finds(a, b)
	clitest.Finds(b, a)
	clitest.Trust(t, a, b, "peer")
	clitest.Trust(t, b, a, "peer")
}

// networkOf returns the network a plugin of the node of s gets, and the node
// called name as it finds it.
func networkOf(t *testing.T, s cli.Settings, name string) (*plugin.Network, plugin.Node) {
	t.Helper()
	app, _ := clitest.NewApp(t, s, "")
	network, ok := cli.Service[*plugin.Network](app)
	if !ok {
		t.Fatal("no network")
	}
	node, err := network.Resolve(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return network, node
}

// next is the next message of ch, for a test that must not wait forever.
func next(t *testing.T, ch *plugin.Channel) ([]byte, error) {
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

// talkTo opens flow/talk on node as the node of s, which is greeted there.
func talkTo(t *testing.T, s cli.Settings, network *plugin.Network, node plugin.Node) *plugin.Channel {
	t.Helper()
	ch, err := network.Open(context.Background(), node, "flow/talk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	if msg, err := next(t, ch); err != nil || string(msg) != "hello "+clitest.PublicKey(t, s) {
		t.Fatalf("got %q, %v", msg, err)
	}
	return ch
}

// The plugin of one node adds a channel, the plugin of another node opens
// it, in either direction: over the same signed and pinned connection as
// every message. Both ends then talk until one of them closes the channel,
// and carry more than a request may.
func TestPluginOpensTheChannelOfAPeer(t *testing.T) {
	beta, onBeta, _ := flowNode(t, "beta")
	alpha, onAlpha, _ := flowNode(t, "alpha")
	connect(t, alpha, beta)

	for _, way := range []struct {
		from, to cli.Settings
		name     string
		handler  *flow
	}{{alpha, beta, "beta", onBeta}, {beta, alpha, "alpha", onAlpha}} {
		network, node := networkOf(t, way.from, way.name)
		// The handler learned who opened the channel from the signature.
		ch := talkTo(t, way.from, network, node)
		if ch.Peer() != clitest.PublicKey(t, way.to) {
			t.Fatalf("the peer of the channel to %s is %q", way.name, ch.Peer())
		}
		for _, msg := range []string{"one", "two"} {
			if err := ch.Send([]byte(msg)); err != nil {
				t.Fatal(err)
			}
			if got, err := next(t, ch); err != nil || string(got) != string(bytes.ToUpper([]byte(msg))) {
				t.Fatalf("to %s: got %q, %v", way.name, got, err)
			}
		}
		if err := ch.Close(); err != nil {
			t.Fatal(err)
		}
		if err := way.handler.why(t); err != io.EOF {
			t.Fatalf("the handler of %s ended with %v", way.name, err)
		}
	}
	if onAlpha.opened.Load() != 1 || onBeta.opened.Load() != 1 {
		t.Fatalf("opened %d times on alpha, %d times on beta", onAlpha.opened.Load(), onBeta.opened.Load())
	}

	// A method carries 1 MiB, a channel as many pieces as there are.
	network, node := networkOf(t, alpha, "beta")
	ch, err := network.Open(context.Background(), node, "flow/sum")
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	piece, sum := bytes.Repeat([]byte("ouroboros"), 50_000), sha256.New()
	for range 6 { // 2.7 MB
		if err := ch.Send(piece); err != nil {
			t.Fatal(err)
		}
		sum.Write(piece)
	}
	if err := ch.Send(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := next(t, ch); err != nil || !bytes.Equal(got, sum.Sum(nil)) {
		t.Fatalf("checksum %x, %v", got, err)
	}
	if _, err := next(t, ch); err != io.EOF {
		t.Fatalf("after the handler returned: %v", err)
	}

	// What decides about trust is no channel, whoever adds one of its name,
	// and a channel nobody added is none.
	for _, name := range []string{a2a.MethodSend, a2a.MethodAccept, a2a.MethodReject, a2a.MethodOpen} {
		if _, err := network.Open(context.Background(), node, name); !errors.Is(err, a2a.ErrBuiltin) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var refused *plugin.Error
	if _, err := network.Open(context.Background(), node, "flow/unknown"); !errors.As(err, &refused) || refused.Code != plugin.CodeMethodMissing {
		t.Errorf("a channel nobody added: %v", err)
	}
	if onBeta.builtin.Load() != 0 {
		t.Errorf("the channel with a built-in name was opened %d times", onBeta.builtin.Load())
	}
}

// Who is not connected does not get to the channel of a plugin, from either
// side: this node opens none to a node its owner does not trust, and the
// node that is asked refuses whoever its owner does not trust.
func TestPluginChannelIsForConnectedPeersOnly(t *testing.T) {
	beta, onBeta, _ := flowNode(t, "beta")
	ctx := context.Background()
	configured := func() cli.Settings {
		s := clitest.Settings(t)
		clitest.Finds(s, beta)
		if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// A node whose owner does not trust beta sends nothing, not even when
	// beta would open the channel.
	stranger := configured()
	clitest.Trust(t, beta, stranger, "stranger")
	network, node := networkOf(t, stranger, "beta")
	if _, err := network.Open(ctx, node, "flow/talk"); !errors.Is(err, plugin.ErrNotConnected) {
		t.Fatalf("a node that does not trust the peer: %v", err)
	}

	// A node that trusts beta, but is not trusted by beta, is refused there.
	// So is a node beta's owner refused.
	eager, blocked := configured(), configured()
	contactsOf := home.Dir(beta.Home).Contacts()
	if err := contactsOf.AddIncoming(clitest.PublicKey(t, blocked), "blocked", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := contactsOf.Reject(clitest.PublicKey(t, blocked)); err != nil || !ok {
		t.Fatalf("reject: %v, %v", ok, err)
	}
	var refused *plugin.Error
	for name, s := range map[string]cli.Settings{"a node beta does not know": eager, "a node beta refused": blocked} {
		clitest.Trust(t, s, beta, "beta")
		network, node := networkOf(t, s, "beta")
		if _, err := network.Open(ctx, node, "flow/talk"); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// So is whoever signs a request by hand, with a key beta does not know.
	forger := &a2a.Client{Identity: identity.Generate()}
	url := fmt.Sprintf("https://127.0.0.1:%d/", clitest.Port(t, beta))
	if _, err := forger.Open(ctx, url, clitest.PublicKey(t, beta), "flow/talk"); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
		t.Fatalf("an unknown key: %v", err)
	}
	// And a request that is signed for another node than beta.
	if _, err := forger.Open(ctx, url, identity.Generate().Public(), "flow/talk"); !errors.Is(err, plugin.ErrUnverified) {
		t.Fatalf("a request for another node: %v", err)
	}

	if onBeta.opened.Load() != 0 {
		t.Fatalf("beta's channel was opened %d times", onBeta.opened.Load())
	}
}

// A node that announces the key of a connected peer at its own address does
// not get the channel: the connection is pinned to the key, which it does
// not hold.
func TestPluginChannelReachesOnlyTheHolderOfTheKey(t *testing.T) {
	beta, onBeta, _ := flowNode(t, "beta")
	mallory, onMallory, _ := flowNode(t, "mallory")
	alpha := clitest.Settings(t)
	if err := config.Save(alpha.Home, clitest.NodeConfig(alpha)); err != nil {
		t.Fatal(err)
	}
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")
	clitest.Trust(t, mallory, alpha, "alpha") // mallory would open it for alpha

	// Mallory answers discovery as beta: with beta's ID, name and key, and
	// the port of its own node.
	impostor := clitest.Settings(t)
	udp, err := discovery.Listen(impostor.DiscoPort)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go discovery.Serve(udp, func() discovery.Node {
		return discovery.Node{
			ID: clitest.NodeID(t, beta), Name: "beta", Model: "gpt-4o",
			PublicKey: clitest.PublicKey(t, beta), Port: clitest.Port(t, mallory),
		}
	})
	clitest.Finds(alpha, impostor)

	network, node := networkOf(t, alpha, "beta")
	if _, err := network.Open(context.Background(), node, "flow/talk"); !errors.Is(err, plugin.ErrUnverified) {
		t.Fatalf("got %v", err)
	}
	if onMallory.opened.Load() != 0 || onBeta.opened.Load() != 0 {
		t.Fatalf("the channel was opened: mallory %d, beta %d", onMallory.opened.Load(), onBeta.opened.Load())
	}

	// Found where it really is, beta opens the channel.
	clitest.Finds(alpha, beta)
	network, node = networkOf(t, alpha, "beta")
	talkTo(t, alpha, network, node)
}

// The channels of a peer are closed when the owner is no longer connected to
// it, without the node being told: the contacts are changed by another
// process, here as the terminal UI would.
func TestChannelClosesWhenTrustIsWithdrawn(t *testing.T) {
	beta, onBeta, _ := flowNode(t, "beta")
	alpha, gamma := clitest.Settings(t), clitest.Settings(t)
	var channels []*plugin.Channel
	for _, s := range []cli.Settings{alpha, gamma} {
		if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
			t.Fatal(err)
		}
		connect(t, s, beta)
		network, node := networkOf(t, s, "beta")
		channels = append(channels, talkTo(t, s, network, node))
	}
	ofAlpha, ofGamma := channels[0], channels[1]

	// Beta's owner drops alpha from the list.
	path := filepath.Join(beta.Home, "contacts.json")
	list, err := home.Dir(beta.Home).Contacts().Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var kept []contacts.Contact
	for _, c := range list.Trusted {
		if c.PublicKey != clitest.PublicKey(t, alpha) {
			kept = append(kept, c)
		}
	}
	list.Trusted = kept
	b, _ := json.Marshal(list)
	if err := os.WriteFile(path+".new", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}

	if _, err := next(t, ofAlpha); !errors.Is(err, plugin.ErrDisconnected) {
		t.Fatalf("the channel of the peer that was dropped: %v", err)
	}
	if err := onBeta.why(t); !errors.Is(err, plugin.ErrDisconnected) {
		t.Fatalf("its handler ended with %v", err)
	}
	// It gets no new one, and the channel of the other peer is not harmed.
	network, node := networkOf(t, alpha, "beta")
	var refused *plugin.Error
	if _, err := network.Open(context.Background(), node, "flow/talk"); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
		t.Fatalf("a channel without trust: %v", err)
	}
	if err := ofGamma.Send([]byte("still here")); err != nil {
		t.Fatal(err)
	}
	if msg, err := next(t, ofGamma); err != nil || string(msg) != "STILL HERE" {
		t.Fatalf("the channel of another peer: %q, %v", msg, err)
	}
}

// A node that stops closes its channels: the ones peers opened to it, and
// the ones its plugins opened to peers.
func TestNodeStopClosesChannels(t *testing.T) {
	beta, _, stopBeta := flowNode(t, "beta")
	alpha, onAlpha, _ := flowNode(t, "alpha")
	connect(t, alpha, beta)
	network, node := networkOf(t, alpha, "beta")

	// Alpha opens a channel to beta, and beta's plugin one back to alpha.
	ch, err := network.Open(context.Background(), node, "flow/back")
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if msg, err := next(t, ch); err != nil || string(msg) != "hello "+clitest.PublicKey(t, beta) {
		t.Fatalf("got %q, %v", msg, err)
	}
	plain := talkTo(t, alpha, network, node)

	stopBeta()
	for name, ch := range map[string]*plugin.Channel{"flow/back": ch, "flow/talk": plain} {
		if _, err := next(t, ch); !errors.Is(err, plugin.ErrStopped) {
			t.Fatalf("%s of a node that stopped: %v", name, err)
		}
	}
	if err := onAlpha.why(t); !errors.Is(err, plugin.ErrStopped) {
		t.Fatalf("the channel the node that stopped had opened: %v", err)
	}
}
