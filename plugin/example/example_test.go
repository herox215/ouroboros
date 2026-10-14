package example_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// contributions is ouro for the node of s with the example plugin, as the
// context to read what it contributed.
func contributions(t *testing.T, s cli.Settings) *plugin.Context {
	t.Helper()
	var ctx *plugin.Context
	clitest.NewApp(t, s, "", example.Plugin(), plugin.New("reader", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	}))
	return ctx
}

// The agent greets a peer with the tool of the plugin, and its owner is shown
// whom it greets.
func TestAgentGreetsAPeer(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	clitest.StartNode(t, beta, cfg, example.Plugin())
	alpha := clitest.Settings(t)
	clitest.Finds(alpha, beta)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")
	ctx := contributions(t, alpha)

	tools := plugin.All[plugin.Tool](ctx)
	at := slices.IndexFunc(tools, func(tool plugin.Tool) bool { return tool.Name == example.Tool })
	if at < 0 {
		t.Fatalf("tools %+v", tools)
	}
	call := json.RawMessage(`{"peer":"beta","from":"Ada"}`)
	if got, err := tools[at].Run(context.Background(), call); err != nil || !strings.HasPrefix(got, "hello Ada, I know you by the key ") {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := tools[at].Run(context.Background(), json.RawMessage(`{"peer":"nobody","from":"Ada"}`)); err == nil {
		t.Fatal("a peer that is not there cannot be greeted")
	}

	for args, want := range map[string]string{string(call): "greeting beta", `{}`: "", `"beta"`: ""} {
		if got := plugin.ToolLine(ctx, plugin.Step{Tool: example.Tool, Args: json.RawMessage(args)}); got != want {
			t.Errorf("shown of %s: %q, want %q", args, got, want)
		}
	}
}

// The plugin shows the connected peers on a screen of the terminal UI, where
// a key greets them all.
func TestGreetingsScreen(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	clitest.StartNode(t, beta, cfg, example.Plugin())
	alpha := clitest.Settings(t)
	clitest.Finds(alpha, beta)
	ctx := contributions(t, alpha)

	// Other plugins of the standard ouro add screens of their own.
	screens := slices.DeleteFunc(plugin.All[plugin.Screen](ctx), func(s plugin.Screen) bool { return s.Command != "greetings" })
	if len(screens) != 1 || screens[0].Command != "greetings" || screens[0].Title != "Greetings" || len(screens[0].Actions) != 1 {
		t.Fatalf("screens %+v", screens)
	}
	screen, greetAll := screens[0].Body, screens[0].Actions[0].Run
	shows := func(want string) {
		t.Helper()
		if got, err := screen(context.Background()); err != nil || got != want {
			t.Fatalf("the screen shows %q, %v, want %q", got, err, want)
		}
	}
	greeted := func(want string) {
		t.Helper()
		if got, err := greetAll(context.Background()); err != nil || got != want {
			t.Fatalf("got %q, %v, want %q", got, err, want)
		}
	}

	shows("No connected peers to greet.")
	greeted("greeted 0 peers")

	// Beta is connected for alpha, but refuses a node its owner does not trust.
	clitest.Trust(t, alpha, beta, "beta")
	shows("beta")
	greeted("greeted 1 peers")
	shows("beta: no answer")

	clitest.Trust(t, beta, alpha, "alpha")
	greeted("greeted 1 peers")
	shows("beta: hello the terminal UI, I know you by the key " + clitest.PublicKey(t, alpha)[:8])

	// Contacts that cannot be read: nobody is known to be connected.
	file := filepath.Join(alpha.Home, "contacts.json")
	os.Remove(file)
	os.Mkdir(file, 0o700)
	if _, err := screen(context.Background()); err == nil {
		t.Fatal("the screen should say that the peers cannot be read")
	}
	if _, err := greetAll(context.Background()); err == nil {
		t.Fatal("the action should say that the peers cannot be read")
	}
}

// The plugin needs the console and the network: without the plugins that
// provide them ouro does not start.
func TestNeedsTheNetwork(t *testing.T) {
	if _, err := cli.NewWith(example.Plugin()); err == nil || !strings.Contains(err.Error(), "plugin example needs *plugin.Network") {
		t.Fatalf("got %v", err)
	}
}
