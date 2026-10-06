package plugin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
	"ouroboros/internal/peers"
	"ouroboros/plugin"
)

type word string

// callOnce asks for the tool "conversation" and answers with its result.
type callOnce struct{}

func (callOnce) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	if last := msgs[len(msgs)-1]; last.Role == "tool" {
		return openai.Message{Content: last.Content}, nil
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "conversation", Arguments: "{}"}}}}, nil
}

// A plugin declares what it needs in the struct its start function takes and
// what it provides in the one it returns; it is started after its provider.
func TestPluginsNeedAndProvide(t *testing.T) {
	var ctx *plugin.Context
	user := plugin.New("user", func(c *plugin.Context, n struct{ Word word }) error {
		ctx = c
		plugin.Add(c, n.Word+"!")
		return nil
	})
	provider := plugin.NewService("provider", func(c *plugin.Context, _ struct{}) (struct{ Word word }, error) {
		plugin.Add(c, word("first"))
		return struct{ Word word }{"hello"}, nil
	})
	k := kernel.New()
	if err := k.Start(user, provider); err != nil {
		t.Fatal(err)
	}
	if got := plugin.All[word](ctx); !slices.Equal(got, []word{"first", "hello!"}) {
		t.Fatalf("items %v", got)
	}

	boom := errors.New("boom")
	failing := plugin.New("failing", func(*plugin.Context, struct{}) error { return boom })
	if err := kernel.New().Start(failing); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if err := kernel.New().Start(plugin.New[struct{}]("empty", nil)); err == nil {
		t.Fatal("a plugin without a start function should fail")
	}
	if err := kernel.New().Start(user); err == nil || !strings.Contains(err.Error(), "plugin user needs plugin_test.word, which no plugin provides") {
		t.Fatalf("got %v", err)
	}
}

func TestConsole(t *testing.T) {
	var out bytes.Buffer
	c := plugin.NewConsole(plugin.Terminal{In: strings.NewReader("  one \n\nlast"), Out: &out, Interactive: true})
	if !c.Interactive || c.Out != &out {
		t.Fatalf("console %+v", c)
	}
	if got, err := c.ReadLine(); err != nil || got != "one" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := c.Prompt("name: ", "default"); err != nil || got != "default" || out.String() != "name: " {
		t.Fatalf("got %q, %v, printed %q", got, err, out.String())
	}
	// Without a way to hide it, a secret is read like any line; the last
	// line needs no line break.
	if got, err := c.ReadSecret("key: "); err != nil || got != "last" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := c.ReadLine(); err != io.EOF {
		t.Fatalf("at the end of the input: %v", err)
	}

	c = plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: &out, ReadSecret: func(prompt string) (string, error) {
		return "hidden " + prompt, nil
	}})
	if got, err := c.ReadSecret("key"); err != nil || got != "hidden key" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestConverse(t *testing.T) {
	var out bytes.Buffer
	c := plugin.NewConsole(plugin.Terminal{In: strings.NewReader("hi\n\nfail\n"), Out: &out})
	err := c.Converse("talking", func(line string) (string, error) {
		if line == "fail" {
			return "", errors.New("no answer")
		}
		return "you said " + line, nil
	})
	want := "talking (Ctrl-D to quit)\n› you said hi\n\n› › ✗ no answer\n\n› \n"
	if err != nil || out.String() != want {
		t.Fatalf("got %q, %v", out.String(), err)
	}

	broken := errors.New("broken")
	c = plugin.NewConsole(plugin.Terminal{In: iotest.ErrReader(broken), Out: io.Discard})
	if err := c.Converse("talking", nil); !errors.Is(err, broken) {
		t.Fatalf("input that cannot be read: %v", err)
	}
}

func TestContributions(t *testing.T) {
	tool := plugin.NewTool("shout", "Returns the text in capitals.", []plugin.Param{{Name: "text"}},
		func(_ context.Context, args struct {
			Text string `json:"text"`
		}) (string, error) {
			return strings.ToUpper(args.Text), nil
		})
	if got, err := tool.Run(context.Background(), json.RawMessage(`{"text":"hi"}`)); err != nil || got != "HI" || tool.Name != "shout" {
		t.Fatalf("tool %+v: %q, %v", tool, got, err)
	}

	if m := plugin.Reply("pong"); m.Text() != "pong" || m.Role != "agent" {
		t.Fatalf("reply %+v", m)
	}

	ctx := context.Background()
	if id := plugin.ContextID(ctx); id != "" {
		t.Fatalf("outside an answer the conversation is %q", id)
	}
	conversation := plugin.NewTool("conversation", "", nil, func(ctx context.Context, _ struct{}) (string, error) {
		return plugin.ContextID(ctx), nil
	})
	if got, _ := agent.New(callOnce{}, "m", "s", conversation).Respond(ctx, "c-1", "hi"); got != "c-1" {
		t.Fatalf("the conversation of a tool: %q", got)
	}
	if _, ok := plugin.OriginFrom(ctx); ok {
		t.Fatal("the owner's context has no origin")
	}
	if o, ok := plugin.OriginFrom(a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "pk"})); !ok || o.PublicKey != "pk" {
		t.Fatalf("origin %+v, %v", o, ok)
	}

	if got := plugin.Label(plugin.Node{Name: "alpha"}); got != "alpha" {
		t.Fatalf("label %q", got)
	}
	if a, b := plugin.NewID(), plugin.NewID(); a == b || len(a) != 36 {
		t.Fatalf("IDs %q, %q", a, b)
	}
}

// finder finds the nodes it consists of.
type finder []discovery.Node

func (f finder) Find(context.Context) ([]discovery.Node, error) { return f, nil }

// The network a plugin gets is the network of the node, without the contacts
// and the client in it. How it treats other nodes is tested in package peers.
func TestNetwork(t *testing.T) {
	noClient := errors.New("no client")
	alpha := discovery.Node{ID: "abc123", Name: "alpha", PublicKey: "pk-alpha", Host: "127.0.0.1", Port: 1}
	n := plugin.NetworkOf(&peers.Network{
		Finder:   finder{alpha},
		Contacts: contacts.Open(filepath.Join(t.TempDir(), "contacts.json")),
		Client:   func() (*a2a.Client, error) { return nil, noClient },
	})
	ctx := context.Background()
	if nodes, err := n.Find(ctx); err != nil || len(nodes) != 1 || nodes[0] != alpha {
		t.Fatalf("find: %+v, %v", nodes, err)
	}
	if found, err := n.Peers(ctx); err != nil || len(found) != 1 || found[0].State != plugin.NotConnected {
		t.Fatalf("peers: %+v, %v", found, err)
	}
	if node, err := n.Resolve(ctx, "abc"); err != nil || node != alpha {
		t.Fatalf("resolve: %+v, %v", node, err)
	}
	if _, err := n.Send(ctx, alpha, "ctx", "hi"); !errors.Is(err, noClient) {
		t.Fatalf("send: %v", err)
	}
	if _, err := n.Call(ctx, alpha, "example/greet", nil); !errors.Is(err, plugin.ErrNotConnected) {
		t.Fatalf("call of a node that is not connected: %v", err)
	}
}
