package example_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/plugin"
	"ouroboros/plugin/example"
)

// Two nodes with the plugin: the one adds the method, the other runs it over
// the network, as soon as their owners are connected and not before.
func TestGreetAPeer(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	clitest.StartNode(t, beta, cfg, example.Plugin())

	alpha := clitest.Settings(t)
	clitest.Finds(alpha, beta)
	if err := config.Save(alpha.Home, clitest.NodeConfig(alpha)); err != nil {
		t.Fatal(err)
	}
	greet := func(args ...string) (string, error) {
		t.Helper()
		app, out := clitest.NewApp(t, alpha, "", example.Plugin())
		err := app.Run(context.Background(), append([]string{"greet"}, args...))
		return out.String(), err
	}

	// Alpha is not connected to beta: nothing is sent.
	if _, err := greet("beta", "Ada"); !errors.Is(err, plugin.ErrNotConnected) {
		t.Fatalf("not connected: %v", err)
	}
	// Alpha's owner trusts beta, but beta's owner does not trust alpha: beta
	// refuses the call before the plugin gets it.
	clitest.Trust(t, alpha, beta, "beta")
	var refused *plugin.Error
	if _, err := greet("beta", "Ada"); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
		t.Fatalf("not trusted by the peer: %v", err)
	}

	clitest.Trust(t, beta, alpha, "alpha")
	want := "hello Ada, I know you by the key " + clitest.PublicKey(t, alpha)[:8] + "\n"
	if out, err := greet("beta", "Ada"); err != nil || out != want {
		t.Fatalf("got %q, %v, want %q", out, err, want)
	}

	// What the method refuses comes back as its error.
	if _, err := greet("beta", ""); !errors.As(err, &refused) || refused.Code != plugin.CodeInvalidParams {
		t.Fatalf("without a name: %v", err)
	}
	if _, err := greet("beta"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("without arguments: %v", err)
	}
	if _, err := greet("nobody", "Ada"); err == nil || !strings.Contains(err.Error(), `no node "nobody"`) {
		t.Fatalf("unknown node: %v", err)
	}
}

// The plugin needs the console and the network: without the plugins that
// provide them ouro does not start.
func TestNeedsTheNetwork(t *testing.T) {
	if _, err := cli.NewWith(example.Plugin()); err == nil || !strings.Contains(err.Error(), "plugin example needs *plugin.Network") {
		t.Fatalf("got %v", err)
	}
}
