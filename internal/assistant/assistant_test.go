package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

func names(tools []agent.Tool) string {
	var s []string
	for _, t := range tools {
		s = append(s, t.Name)
	}
	return strings.Join(s, ",")
}

func TestPick(t *testing.T) {
	all := PeerTools(nil, func() string { return "abc123" })
	tests := []struct {
		allowed []config.Entry
		want    string
	}{
		{nil, ""},
		{config.Named(), ""},
		{config.Named(config.AllTools), "list_peers,ask_peer,answer_peer,tell_peer,ask_back"},
		{config.Named("ask_peer"), "ask_peer"},
		{config.Named("ask_peer", "list_peers"), "list_peers,ask_peer"},
		{config.Named(config.AllTools, "ask_peer"), "list_peers,ask_peer,answer_peer,tell_peer,ask_back"},
	}
	for _, tt := range tests {
		got, err := pick(all, tt.allowed, forOwner)
		if err != nil || names(got) != tt.want {
			t.Errorf("pick(%v) = %q, %v", tt.allowed, names(got), err)
		}
	}
	// A typo must not silently take a tool away.
	if _, err := pick(all, config.Named("ask_peers"), forOwner); err == nil || !strings.Contains(err.Error(), `"ask_peers"`) {
		t.Fatalf("unknown tool: %v", err)
	}
}

// An entry with settings configures its tool, next to "*" as well. A tool
// that takes none, a tool that is listed twice and settings for every tool at
// once are errors.
func TestPickWithSettings(t *testing.T) {
	type settings struct {
		Greeting string `json:"greeting"`
	}
	greet := agent.Configurable(agent.Tool{Name: "greet"}, func(s settings) (agent.RunFunc, error) {
		return func(context.Context, json.RawMessage) (string, error) { return s.Greeting + "!", nil }, nil
	})
	plain := agent.NewTool("plain", "", nil, func(context.Context, struct{}) (string, error) { return "plain", nil })
	all := []agent.Tool{plain, greet}
	with := func(tool, settings string) config.Entry {
		return config.Entry{Tool: tool, Settings: json.RawMessage(settings)}
	}

	for _, tt := range []struct {
		allowed []config.Entry
		names   string
		says    string
	}{
		{config.Named("*"), "plain,greet", "!"},
		{config.Named("greet"), "greet", "!"},
		{[]config.Entry{with("greet", `{"greeting": "hi"}`)}, "greet", "hi!"},
		{[]config.Entry{{Tool: "*"}, with("greet", `{"greeting": "hi"}`)}, "plain,greet", "hi!"},
		{[]config.Entry{with("greet", `{"greeting": "hi"}`), {Tool: "*"}}, "plain,greet", "hi!"},
		{[]config.Entry{with("greet", `{}`), with("plain", `{}`)}, "plain,greet", "!"},
		{[]config.Entry{with("plain", ``)}, "plain", ""},
	} {
		got, err := pick(all, tt.allowed, forOwner)
		if err != nil || names(got) != tt.names {
			t.Errorf("pick(%s) = %q, %v", tt.allowed, names(got), err)
			continue
		}
		last := got[len(got)-1]
		if says, _ := last.Run(context.Background(), nil); last.Name == "greet" && says != tt.says {
			t.Errorf("pick(%s): greet says %q, want %q", tt.allowed, says, tt.says)
		}
	}

	for _, tt := range []struct {
		allowed []config.Entry
		want    string
	}{
		{[]config.Entry{with("plain", `{"greeting": "hi"}`)}, "the tool plain takes no settings"},
		{[]config.Entry{{Tool: "*"}, with("plain", `{"x": 1}`)}, "the tool plain takes no settings"},
		{[]config.Entry{with("greet", `{"greting": "hi"}`)}, `settings of the tool greet: json: unknown field "greting"`},
		{[]config.Entry{with("greet", `{"greeting": 1}`)}, "settings of the tool greet: json: cannot unmarshal number into Go struct field settings.greeting of type string"},
		{config.Named("greet", "greet"), `the tool "greet" is listed twice`},
		{[]config.Entry{{Tool: "greet"}, with("greet", `{}`)}, `the tool "greet" is listed twice`},
		{config.Named("*", "plain", "*"), `the tool "*" is listed twice`},
		{[]config.Entry{with("*", `{"greeting": "hi"}`)}, `"*" stands for every tool and takes no settings: list the tool they are for`},
		{[]config.Entry{with("*", `{}`)}, `"*" stands for every tool and takes no settings: list the tool they are for`},
		{[]config.Entry{with("nope", `{}`)}, `unknown tool "nope" (there are: plain, greet)`},
		{config.Named(""), `unknown tool "" (there are: plain, greet)`},
	} {
		if got, err := pick(all, tt.allowed, forOwner); err == nil || err.Error() != tt.want || got != nil {
			t.Errorf("pick(%s) = %q, %v; want %q", tt.allowed, names(got), err, tt.want)
		}
	}
}

// The agent that answers peers is told about its tools when it has some, and
// about the rules for paths when it has a tool for files.
func TestPeerPromptFollowsTheTools(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	file := agent.NewTool("read", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	bash := agent.NewTool(ToolBash, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return append(PeerTools(nil, func() string { return "" }), file, bash) }}
	for _, tt := range []struct {
		tools []config.Entry
		want  string
	}{
		{nil, `You are "me", an Ouroboros agent reachable via A2A. Be helpful and concise.`},
		{config.Named(toolListPeers), `You are "me", an Ouroboros agent reachable via A2A. Be helpful and concise.` + toolsHint},
		{config.Named("read"), `You are "me", an Ouroboros agent reachable via A2A. Be helpful and concise.` + toolsHint + peerFilesHint},
		{config.Named("read", ToolBash), `You are "me", an Ouroboros agent reachable via A2A. Be helpful and concise.` + toolsHint + peerFilesHint + exploreHint},
	} {
		cfg.Tools = &config.Tools{A2A: tt.tools}
		bot, err := b.ForPeers(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := bot.Respond(context.Background(), "chat", "?system"); err != nil || got != tt.want {
			t.Errorf("with %s: %v\n got %q\nwant %q", tt.tools, err, got, tt.want)
		}
	}
}

func TestUnknownToolFailsEarly(t *testing.T) {
	cfg := config.Config{Access: "http://x", Model: "m", Name: "n"}
	b := Builder{Tools: func() []agent.Tool { return nil }}
	cfg.Tools = &config.Tools{A2A: config.Named("nope")}
	if _, err := b.ForPeers(cfg); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("ForPeers: %v", err)
	}
	cfg.Tools = &config.Tools{H2A: config.Named("nope")}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	if err := a.Ready(); err == nil || !strings.Contains(err.Error(), "tools.h2a") {
		t.Fatalf("Ready: %v", err)
	}
}

// The owner picks the tools of the lists from what the plugins contributed,
// the tool that asks the owner and the one that hands tasks to helpers, and
// the tools of the configuration where the node has one.
func TestAvailable(t *testing.T) {
	plain := agent.NewTool("plain", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	b := Builder{Tools: func() []agent.Tool { return []agent.Tool{plain} }}
	want := "plain," + ToolUserQuestion + "," + ToolDelegate
	if got := names(b.Available()); got != want {
		t.Errorf("without a node: %s, want %s", got, want)
	}
	b = b.WithConfig(func() (config.Config, error) { return config.Config{}, nil }, nil)
	want += "," + toolGetConfig + "," + toolProposeConfig + "," + toolWithdrawConfig
	if got := names(b.Available()); got != want {
		t.Errorf("with a node: %s, want %s", got, want)
	}
}

// A conversation that is forgotten starts anew, and its last answer is no
// answer a message follows up on.
func TestForget(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServer(t), Model: "gpt-4o", Name: "me"}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }}, discard)
	ask := func(want string) {
		t.Helper()
		if got, err := a.Ask(context.Background(), "chat", "hello", nil); err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	ask("[gpt-4o] turns=1 last=hello")
	ask("[gpt-4o] turns=3 last=hello")
	a.keep("chat", answered{at: time.Now()})
	a.keep("other", answered{at: time.Now()})
	a.Forget("chat")
	if _, _, ok := a.followed("chat", "?", time.Now()); ok {
		t.Error("a forgotten answer is followed up on")
	}
	if _, _, ok := a.followed("other", "?", time.Now()); !ok {
		t.Error("forgetting one conversation forgot another")
	}
	ask("[gpt-4o] turns=1 last=hello")
}

// A catalog of tools that cannot be expanded is offered as it is: building
// the agent reports it.
func TestAvailableWithAnInvalidCatalog(t *testing.T) {
	plain := agent.NewTool("plain", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	loader := agent.NewTool("loader", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	loader.AbilityCatalog = func() []agent.Ability { return nil }
	b := Builder{Tools: func() []agent.Tool { return []agent.Tool{plain, plain, loader} }}
	want := "plain,plain,loader," + ToolUserQuestion + "," + ToolDelegate
	if got := names(b.Available()); got != want {
		t.Errorf("an invalid catalog: %s, want %s", got, want)
	}
	cfg := config.Config{Access: "http://x", Model: "m", Name: "n", Tools: &config.Tools{A2A: config.Named("plain")}}
	if _, err := b.ForPeers(cfg); err == nil || !strings.Contains(err.Error(), "duplicate tool") {
		t.Fatalf("ForPeers: %v", err)
	}
}
