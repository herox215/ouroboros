package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

// overHTTP is the LLM of a node as it runs: the API at the endpoint.
func overHTTP(at agent.Endpoint) agent.LLM { return openai.New(at.BaseURL, at.APIKey) }

// The assistant answers its owner with the LLM the node is configured for,
// asks peers on the way and reports which of them it is waiting for.
func TestAsk(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.found.nodes = append(s.found.nodes, s.node())
	s.connect(t, luna)
	cfg := config.Config{BaseURL: openaitest.NewServer(t), APIKey: openaitest.Key, Model: "gpt-4o", Name: "me", PrivateKey: s.id.Private()}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:   overHTTP,
		Tools: func() []agent.Tool { return PeerTools(s.network, s.id.ID) },
	})
	if err := a.Ready(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var steps []Step
	observe := func(s Step) { steps = append(steps, s) }

	got, err := a.Ask(ctx, "chat-1", "hello", observe)
	if err != nil || got != "[gpt-4o] turns=1 last=hello" || len(steps) != 0 {
		t.Fatalf("got %q, %v with steps %+v", got, err, steps)
	}

	// The answer of the peer goes to the LLM; the owner sees who is asked.
	got, err = a.Ask(ctx, "chat-1", "@luna how are you?", observe)
	if err != nil || got != "[gpt-4o] turns=5 last=luna: how are you?" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !slices.Equal(steps, []Step{{Peer: "luna"}, {Peer: "luna", Done: true}}) {
		t.Fatalf("steps %+v", steps)
	}
	// The peer has one conversation per conversation of the owner.
	if got, want := luna.messages(), "chat-1/"+luna.ID+": how are you?"; got != want {
		t.Fatalf("luna got %q, want %q", got, want)
	}

	// Other tools are used without a word to the owner.
	steps = nil
	got, err = a.Ask(ctx, "chat-2", "@", observe)
	if err != nil || got != "[gpt-4o] turns=3 last=luna (id "+luna.ID+", model gpt-luna, connected): knows the way" || len(steps) != 0 {
		t.Fatalf("got %q, %v with steps %+v", got, err, steps)
	}

	// Nobody has to watch.
	if got, err := a.Ask(ctx, "chat-3", "@luna anyone?", nil); err != nil || got != "[gpt-4o] turns=3 last=luna: anyone?" {
		t.Fatalf("unobserved: %q, %v", got, err)
	}
}

// A node that cannot answer says why, and asks nobody.
func TestAskUnconfigured(t *testing.T) {
	cfg := config.Config{BaseURL: openaitest.NewServer(t), APIKey: openaitest.Key, Model: "gpt-4o", Name: "me"}
	var failed error
	built := 0
	a := New(func() (config.Config, error) { return cfg, failed }, Builder{
		LLM:   func(at agent.Endpoint) agent.LLM { built++; return overHTTP(at) },
		Tools: func() []agent.Tool { return nil },
	})
	ctx := context.Background()

	failed = errTest
	if err := a.Ready(); !errors.Is(err, errTest) {
		t.Fatalf("Ready with a configuration that cannot be read: %v", err)
	}
	if got, err := a.Ask(ctx, "chat", "hello", nil); !errors.Is(err, errTest) || got != "" {
		t.Fatalf("Ask with a configuration that cannot be read: %q, %v", got, err)
	}

	failed = nil
	for field, clear := range map[string]func(*config.Config){
		"base URL": func(c *config.Config) { c.BaseURL = "" },
		"model":    func(c *config.Config) { c.Model = "" },
		"name":     func(c *config.Config) { c.Name = "" },
	} {
		complete := cfg
		clear(&cfg)
		if err := a.Ready(); !errors.Is(err, config.ErrIncomplete) {
			t.Errorf("Ready without %s: %v", field, err)
		}
		if got, err := a.Ask(ctx, "chat", "hello", nil); !errors.Is(err, config.ErrIncomplete) || got != "" {
			t.Errorf("Ask without %s: %q, %v", field, got, err)
		}
		cfg = complete
	}
	if built != 0 {
		t.Fatalf("%d agents were built for a node that is not set up", built)
	}

	if got, err := a.Ask(ctx, "chat", "hello", nil); err != nil || got != "[gpt-4o] turns=1 last=hello" {
		t.Fatalf("set up: %q, %v", got, err)
	}
}

// The assistant remembers its conversations as long as the node is configured
// the same, and is built anew for whatever changes of what it is built from.
func TestAskRemembersUntilConfiguredAnew(t *testing.T) {
	cfg := config.Config{BaseURL: openaitest.NewServerWithKey(t, ""), APIKey: "sk-1", Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()}
	built := 0
	var endpoint agent.Endpoint
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:   func(at agent.Endpoint) agent.LLM { built++; endpoint = at; return overHTTP(at) },
		Tools: func() []agent.Tool { return PeerTools(nil, func() string { return "" }) },
	})
	ctx := context.Background()
	turns := func(want int) {
		t.Helper()
		got, err := a.Ask(ctx, "chat", "hello", nil)
		if suffix := " turns=" + string(rune('0'+want)) + " last=hello"; err != nil || !strings.HasSuffix(got, suffix) {
			t.Fatalf("got %q, %v, want it to end with %q", got, err, suffix)
		}
	}

	turns(1)
	turns(3)
	// What does not concern the agent leaves it alone.
	cfg.Tools = &config.Tools{A2A: []string{config.AllTools}}
	if err := a.Ready(); err != nil {
		t.Fatal(err)
	}
	turns(5)
	if built != 1 || endpoint != (agent.Endpoint{BaseURL: cfg.BaseURL, APIKey: "sk-1"}) {
		t.Fatalf("built %d agents, the last at %+v", built, endpoint)
	}

	changes := []struct {
		what   string
		change func(*config.Config)
	}{
		{"base URL", func(c *config.Config) { c.BaseURL += "/" }},
		{"API key", func(c *config.Config) { c.APIKey = "sk-2" }},
		{"model", func(c *config.Config) { c.Model = "gpt-4o-mini" }},
		{"name", func(c *config.Config) { c.Name = "you" }},
		{"key of the node", func(c *config.Config) { c.PrivateKey = identity.Generate().Private() }},
		{"tools of the owner", func(c *config.Config) { c.Tools = &config.Tools{H2A: []string{toolListPeers}} }},
	}
	for i, tt := range changes {
		tt.change(&cfg)
		if err := a.Ready(); err != nil {
			t.Fatalf("new %s: %v", tt.what, err)
		}
		if built != i+2 {
			t.Fatalf("new %s: %d agents built, want %d", tt.what, built, i+2)
		}
		turns(1)
		turns(3)
		if built != i+2 {
			t.Fatalf("new %s: %d agents built for one configuration", tt.what, built)
		}
	}
	if endpoint != (agent.Endpoint{BaseURL: cfg.BaseURL, APIKey: "sk-2"}) {
		t.Fatalf("the last agent is at %+v", endpoint)
	}

	// The new agent is the one that is asked: it has the new model, name and tools.
	got, err := a.Ask(ctx, "other", "?system", nil)
	if err != nil || !strings.HasPrefix(got, `You are "you", the personal agent`) || !strings.HasSuffix(got, ownerStyle) || strings.Contains(got, toolAskPeer) {
		t.Fatalf("prompt: %q, %v", got, err)
	}
	if got, err := a.Ask(ctx, "other", "@luna hello", nil); err != nil || got != "[gpt-4o-mini] turns=3 last=@luna hello" {
		t.Fatalf("without ask_peer: %q, %v", got, err)
	}

	// A configuration that is wrong does not replace the agent by nothing...
	cfg.Tools = &config.Tools{H2A: []string{"nope"}}
	if got, err := a.Ask(ctx, "chat", "hello", nil); err == nil || got != "" || !strings.Contains(err.Error(), `tools.h2a: unknown tool "nope"`) {
		t.Fatalf("unknown tool: %q, %v", got, err)
	}
	// ...so that it still knows the conversation when the mistake is undone.
	cfg.Tools = &config.Tools{H2A: []string{toolListPeers}}
	turns(5)
}

// The owner is shown which peer the assistant waits for, and nothing of what
// else it does.
func TestPeerStep(t *testing.T) {
	args := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	asking := args(askPeerArgs{Peer: "luna", Message: "how are you?"})
	tests := []struct {
		name string
		step agent.Step
		want Step
		ok   bool
	}{
		{"asking", agent.Step{Tool: toolAskPeer, Args: asking}, Step{Peer: "luna"}, true},
		{"answered", agent.Step{Tool: toolAskPeer, Args: asking, Done: true, Result: "fine"}, Step{Peer: "luna", Done: true}, true},
		{"failed", agent.Step{Tool: toolAskPeer, Args: asking, Done: true, Err: errTest}, Step{Peer: "luna", Done: true}, true},
		{"another tool", agent.Step{Tool: toolListPeers, Args: asking}, Step{}, false},
		{"no peer", agent.Step{Tool: toolAskPeer, Args: args(askPeerArgs{Message: "anyone?"})}, Step{}, false},
		{"no arguments", agent.Step{Tool: toolAskPeer}, Step{}, false},
		{"arguments that are no text", agent.Step{Tool: toolAskPeer, Args: json.RawMessage(`{"peer": 1}`)}, Step{}, false},
	}
	for _, tt := range tests {
		if got, ok := peerStep(tt.step); got != tt.want || ok != tt.ok {
			t.Errorf("%s: peerStep = %+v, %v, want %+v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
