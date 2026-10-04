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

// shown is what the owner is shown of steps: of a call of ask_peer which peer
// the assistant is waiting for, as the peers plugin contributes it, and
// nothing of any other tool.
func shown(steps []Step) []string {
	var lines []string
	for _, s := range steps {
		if s.Tool == ToolAskPeer {
			lines = append(lines, AskingPeer(s.Args))
		}
	}
	return lines
}

// The assistant answers its owner with the LLM the node is configured for,
// asks peers on the way and reports the tools it calls: of a peer it asks,
// the owner is shown which one it is waiting for.
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
	// The call is reported when it starts and when its result is in, with
	// its arguments and without the answer of the peer.
	asking := `{"message":"how are you?","peer":"luna"}`
	if len(steps) != 2 || steps[0].Tool != toolAskPeer || string(steps[0].Args) != asking || steps[0].Done ||
		steps[1].Tool != toolAskPeer || string(steps[1].Args) != asking || !steps[1].Done {
		t.Fatalf("steps %+v", steps)
	}
	if !slices.Equal(shown(steps), []string{"asking luna", "asking luna"}) {
		t.Fatalf("shown %q", shown(steps))
	}
	// The peer has one conversation per conversation of the owner.
	if got, want := luna.messages(), "chat-1/"+luna.ID+": how are you?"; got != want {
		t.Fatalf("luna got %q, want %q", got, want)
	}

	// Other tools are used without a word to the owner.
	steps = nil
	got, err = a.Ask(ctx, "chat-2", "@", observe)
	if err != nil || got != "[gpt-4o] turns=3 last=luna (id "+luna.ID+", model gpt-luna, connected): knows the way" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(steps) != 2 || steps[0].Tool != toolListPeers || steps[0].Done || steps[1].Tool != toolListPeers || !steps[1].Done || len(shown(steps)) != 0 {
		t.Fatalf("steps %+v, shown %q", steps, shown(steps))
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
	cfg.Tools = &config.Tools{A2A: config.Named(config.AllTools)}
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
		{"settings of a tool", func(c *config.Config) {
			c.Tools = &config.Tools{H2A: []config.Entry{{Tool: toolListPeers, Settings: json.RawMessage(`{}`)}}}
		}},
		{"tools of the owner", func(c *config.Config) { c.Tools = &config.Tools{H2A: config.Named(toolListPeers)} }},
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
	cfg.Tools = &config.Tools{H2A: config.Named("nope")}
	if got, err := a.Ask(ctx, "chat", "hello", nil); err == nil || got != "" || !strings.Contains(err.Error(), `tools.h2a: unknown tool "nope"`) {
		t.Fatalf("unknown tool: %q, %v", got, err)
	}
	// ...so that it still knows the conversation when the mistake is undone.
	cfg.Tools = &config.Tools{H2A: config.Named(toolListPeers)}
	turns(5)
}

// The prompt of the owner's agent says how to use the tools it has, and what
// to make of a peer that asks for a file.
func TestOwnerPromptFollowsTheTools(t *testing.T) {
	cfg := config.Config{BaseURL: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	file := agent.NewTool("edit", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:   overHTTP,
		Tools: func() []agent.Tool { return append(PeerTools(nil, func() string { return "" }), file) },
	})
	start := `You are "me", the personal agent of the owner of this Ouroboros node. `
	for _, tt := range []struct {
		tools []config.Entry
		want  string
	}{
		{config.Named(), start + ownerStyle},
		{config.Named(toolListPeers), start + ownerStyle},
		{config.Named(toolAskPeer), start + askHint + ownerStyle},
		{config.Named("edit"), start + filesHint + ownerStyle},
		{config.Named("*"), start + askHint + filesHint + claimHint + ownerStyle},
	} {
		cfg.Tools = &config.Tools{H2A: tt.tools}
		if got, err := a.Ask(context.Background(), "chat", "?system", nil); err != nil || got != tt.want {
			t.Errorf("with %s: %v\n got %q\nwant %q", tt.tools, err, got, tt.want)
		}
	}
	if strings.Join(fileTools, ",") != "read,write,edit,list,delete" {
		t.Errorf("the tools for files: %v", fileTools)
	}
}

// The owner is shown which peer the assistant waits for, and nothing of what
// it says to the peer.
func TestAskingPeer(t *testing.T) {
	args := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	if ToolAskPeer != "ask_peer" {
		t.Fatalf("the tool that asks a peer is called %q", ToolAskPeer)
	}
	tests := []struct {
		name string
		args json.RawMessage
		want string
	}{
		{"asking", args(askPeerArgs{Peer: "luna", Message: "how are you?"}), "asking luna"},
		{"no peer", args(askPeerArgs{Message: "anyone?"}), ""},
		{"no arguments", nil, ""},
		{"arguments that are no text", json.RawMessage(`{"peer": 1}`), ""},
	}
	for _, tt := range tests {
		if got := AskingPeer(tt.args); got != tt.want {
			t.Errorf("%s: AskingPeer = %q, want %q", tt.name, got, tt.want)
		}
	}
}
