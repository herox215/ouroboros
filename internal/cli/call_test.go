package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/discovery"
	"ouroboros/internal/identity"
	"ouroboros/plugin"
)

// probe is a plugin that adds the method "probe/echo" to its node and the
// command "ouro probe NODE TEXT" that runs it on another one. It records who
// its method ran for.
type probe struct {
	mu      sync.Mutex
	callers []string // public keys the method ran for
	builtin int      // how often its method with a built-in name ran
}

func (p *probe) ran() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.callers...)
}

// takenOver is how often the method with a built-in name ran.
func (p *probe) takenOver() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.builtin
}

func (p *probe) plugin() plugin.Plugin {
	type needs struct {
		Console *plugin.Console
		Network *plugin.Network
	}
	return plugin.New("probe", func(ctx *plugin.Context, n needs) error {
		plugin.Add(ctx, plugin.Method{Name: "probe/echo", Handle: func(_ context.Context, from plugin.Caller, params json.RawMessage) (*plugin.Message, *plugin.Error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.callers = append(p.callers, from.PublicKey)
			var text string
			json.Unmarshal(params, &text)
			return plugin.Reply("echo " + text), nil
		}})
		// A plugin that tries to take the place of a built-in method.
		plugin.Add(ctx, plugin.Method{Name: a2a.MethodSend, Handle: func(context.Context, plugin.Caller, json.RawMessage) (*plugin.Message, *plugin.Error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.builtin++
			return plugin.Reply("taken over"), nil
		}})
		plugin.Add(ctx, plugin.Command{Name: "probe", Args: "NODE TEXT", Help: "run probe/echo on a node", Run: func(c context.Context, args []string) error {
			node, err := n.Network.Resolve(c, args[0])
			if err != nil {
				return err
			}
			reply, err := n.Network.Call(c, node, "probe/echo", args[1])
			if err != nil {
				return err
			}
			fmt.Fprintln(n.Console.Out, reply.Text())
			return nil
		}})
		return nil
	})
}

// probeNode starts a node called name with the probe plugin.
func probeNode(t *testing.T, name string) (cli.Settings, *probe) {
	t.Helper()
	s, p := clitest.Settings(t), &probe{}
	cfg := clitest.NodeConfig(s)
	cfg.Name = name
	clitest.StartNode(t, s, cfg, p.plugin())
	return s, p
}

// callProbe runs "ouro probe target text" as the node of s.
func callProbe(t *testing.T, s cli.Settings, target, text string) (string, error) {
	t.Helper()
	app, out := clitest.NewApp(t, s, "", (&probe{}).plugin())
	err := app.Run(context.Background(), []string{"probe", target, text})
	return out.String(), err
}

// The plugin of one node adds a method, the plugin of another node calls it:
// over the same signed and pinned connection as every message, and only
// between nodes whose owners are connected.
func TestPluginCallsTheMethodOfAPeer(t *testing.T) {
	beta, onBeta := probeNode(t, "beta")
	alpha, onAlpha := probeNode(t, "alpha")
	clitest.Finds(alpha, beta)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")

	out, err := callProbe(t, alpha, "beta", "hi")
	if err != nil || out != "echo hi\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	// Beta's plugin learned who called from the signature of the request.
	if got := onBeta.ran(); len(got) != 1 || got[0] != clitest.PublicKey(t, alpha) {
		t.Fatalf("beta's method ran for %v", got)
	}
	if len(onAlpha.ran()) != 0 {
		t.Fatal("the method ran on the node that called it")
	}

	// The method that was added under a built-in name never runs: beta's
	// agent answers messages as before.
	out, err = clitest.Run(t, alpha, "", "ask", "beta", "hello")
	if err != nil || out != "[gpt-4o] turns=1 last=hello\n" || onBeta.takenOver() != 0 {
		t.Fatalf("message/send should stay built in: %q, %v, the plugin's method ran %d times", out, err, onBeta.takenOver())
	}
	// And a plugin cannot run the built-in methods by name.
	app, _ := clitest.NewApp(t, alpha, "")
	network, _ := cli.Service[*plugin.Network](app)
	node, err := network.Resolve(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{a2a.MethodSend, a2a.MethodAccept, a2a.MethodReject} {
		if _, err := network.Call(context.Background(), node, method, nil); !errors.Is(err, a2a.ErrBuiltin) {
			t.Errorf("%s: %v", method, err)
		}
	}
}

// Who is not connected does not get to the method of a plugin, from either
// side: this node does not call a node its owner does not trust, and the
// node that is called refuses a caller its owner does not trust.
func TestPluginMethodIsForConnectedPeersOnly(t *testing.T) {
	beta, onBeta := probeNode(t, "beta")

	// A node whose owner does not trust beta sends nothing.
	stranger := clitest.Settings(t)
	clitest.Finds(stranger, beta)
	config.Save(stranger.Home, clitest.NodeConfig(stranger))
	if _, err := callProbe(t, stranger, "beta", "hi"); !errors.Is(err, plugin.ErrNotConnected) {
		t.Fatalf("a node that is not connected: %v", err)
	}
	// Not even when beta would answer it.
	clitest.Trust(t, beta, stranger, "stranger")
	if _, err := callProbe(t, stranger, "beta", "hi"); !errors.Is(err, plugin.ErrNotConnected) {
		t.Fatalf("a node that does not trust the peer: %v", err)
	}

	// A node that trusts beta, but is not trusted by beta, is refused there.
	eager := clitest.Settings(t)
	clitest.Finds(eager, beta)
	config.Save(eager.Home, clitest.NodeConfig(eager))
	clitest.Trust(t, eager, beta, "beta")
	var refused *plugin.Error
	if _, err := callProbe(t, eager, "beta", "hi"); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
		t.Fatalf("a caller the peer does not trust: %v", err)
	}

	// So is whoever signs a request by hand, with a key beta does not know.
	port := clitest.Port(t, beta)
	forger := &a2a.Client{Identity: identity.Generate()}
	url := fmt.Sprintf("https://127.0.0.1:%d/", port)
	if _, err := forger.Call(context.Background(), url, clitest.PublicKey(t, beta), "probe/echo", "hi"); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
		t.Fatalf("an unknown key: %v", err)
	}
	// And a request that is signed for another node than beta.
	if _, err := forger.Call(context.Background(), url, identity.Generate().Public(), "probe/echo", "hi"); !errors.Is(err, plugin.ErrUnverified) {
		t.Fatalf("a request for another node: %v", err)
	}

	if got := onBeta.ran(); len(got) != 0 {
		t.Fatalf("beta's method ran for %v", got)
	}
}

// A node that announces the key of a connected peer at its own address does
// not get the call: the connection is pinned to the key, which it does not
// hold.
func TestPluginCallReachesOnlyTheHolderOfTheKey(t *testing.T) {
	beta, onBeta := probeNode(t, "beta")
	mallory, onMallory := probeNode(t, "mallory")
	alpha := clitest.Settings(t)
	config.Save(alpha.Home, clitest.NodeConfig(alpha))
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")
	clitest.Trust(t, mallory, alpha, "alpha") // mallory would answer alpha

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

	if _, err := callProbe(t, alpha, "beta", "secret"); !errors.Is(err, plugin.ErrUnverified) {
		t.Fatalf("got %v", err)
	}
	if len(onMallory.ran()) != 0 || len(onBeta.ran()) != 0 {
		t.Fatalf("the call reached a node: mallory %v, beta %v", onMallory.ran(), onBeta.ran())
	}

	// Found where it really is, beta gets the call.
	clitest.Finds(alpha, beta)
	if out, err := callProbe(t, alpha, "beta", "secret"); err != nil || !strings.Contains(out, "echo secret") || len(onBeta.ran()) != 1 {
		t.Fatalf("got %q, %v", out, err)
	}
}
