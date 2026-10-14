package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

// accesses is what a test says of the accesses of a node.
type accesses struct {
	rev, label string
	secrets    []string
}

func (a *accesses) Revision(string) string { return a.rev }
func (a *accesses) Label(string) string    { return a.label }
func (a *accesses) Secrets() []string      { return a.secrets }

// overHTTP is the LLM of a node as it runs, for a test: the access is the
// endpoint of the API, which takes the key of the fake one.
func overHTTP(access string) (agent.LLM, error) { return openai.New(access, openaitest.Key), nil }

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
	cfg := config.Config{Access: openaitest.NewServer(t), Model: "gpt-4o", Name: "me", PrivateKey: s.id.Private()}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:   overHTTP,
		Tools: func() []agent.Tool { return PeerTools(s.network, s.id.ID) },
	}, discard)
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
	// The script the call is made in is reported around it.
	if len(steps) != 4 || steps[0].Tool != toolRun || steps[3].Tool != toolRun {
		t.Fatalf("steps %+v", steps)
	}
	steps = steps[1:3]
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
	if len(steps) != 4 || steps[1].Tool != toolListPeers || steps[1].Done || steps[2].Tool != toolListPeers || !steps[2].Done || len(shown(steps)) != 0 {
		t.Fatalf("steps %+v, shown %q", steps, shown(steps))
	}

	// Nobody has to watch.
	if got, err := a.Ask(ctx, "chat-3", "@luna anyone?", nil); err != nil || got != "[gpt-4o] turns=3 last=luna: anyone?" {
		t.Fatalf("unobserved: %q, %v", got, err)
	}
}

// A node that cannot answer says why, and asks nobody.
func TestAskUnconfigured(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServer(t), Model: "gpt-4o", Name: "me"}
	var failed error
	built := 0
	a := New(func() (config.Config, error) { return cfg, failed }, Builder{
		LLM:   func(access string) (agent.LLM, error) { built++; return overHTTP(access) },
		Tools: func() []agent.Tool { return nil },
	}, discard)
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
		"access": func(c *config.Config) { c.Access = "" },
		"model":  func(c *config.Config) { c.Model = "" },
		"name":   func(c *config.Config) { c.Name = "" },
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

// An access whose model cannot be had, as when its provider is turned off,
// is why the node cannot answer.
func TestAskWithoutTheModelOfTheAccess(t *testing.T) {
	cfg := config.Config{Access: "gone", Model: "m", Name: "me"}
	b := Builder{
		LLM: func(access string) (agent.LLM, error) {
			return nil, fmt.Errorf("access %q: its provider is not part of this ouro", access)
		},
		Tools: func() []agent.Tool { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	if got, err := a.Ask(context.Background(), "chat", "hello", nil); err == nil || got != "" || !strings.Contains(err.Error(), `access "gone": its provider is not part of this ouro`) {
		t.Fatalf("Ask = %q, %v", got, err)
	}
	if _, err := b.ForPeers(cfg); err == nil {
		t.Fatal("peers are answered without a model")
	}
}

// The assistant remembers its conversations as long as the node is configured
// the same, and is built anew for whatever changes of what it is built from.
func TestAskRemembersAlsoWhenConfiguredAnew(t *testing.T) {
	first, second := openaitest.NewServerWithKey(t, ""), openaitest.NewServerWithKey(t, "")
	cfg := config.Config{Access: first, Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()}
	built := 0
	var access string
	known := &accesses{rev: "1"}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:      func(id string) (agent.LLM, error) { built++; access = id; return overHTTP(id) },
		Accesses: known,
		Tools:    func() []agent.Tool { return PeerTools(nil, func() string { return "" }) },
	}, discard)
	// What the LLM thought in a conversation, to see what becomes of it.
	thought := func() string {
		for _, m := range a.memory.Conversation("thinking") {
			if m.Reasoning != "" {
				return m.Reasoning
			}
		}
		return ""
	}
	a.memory.Restore("thinking", []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho", Reasoning: "hm", ReasoningField: "reasoning"}})
	ctx := context.Background()
	// next asks in the conversation chat, which goes on with every agent.
	asked := 0
	next := func() {
		t.Helper()
		asked++
		got, err := a.Ask(ctx, "chat", "hello", nil)
		if suffix := fmt.Sprintf(" turns=%d last=hello", 2*asked-1); err != nil || !strings.HasSuffix(got, suffix) {
			t.Fatalf("got %q, %v, want it to end with %q", got, err, suffix)
		}
	}

	next()
	next()
	// What does not concern the agent leaves it alone.
	cfg.Tools = &config.Tools{A2A: config.Named(config.AllTools)}
	if err := a.Ready(); err != nil {
		t.Fatal(err)
	}
	next()
	if built != 1 || access != first {
		t.Fatalf("built %d agents, the last with %q", built, access)
	}

	changes := []struct {
		what   string
		change func(*config.Config)
	}{
		// The owner signed in to the access anew, as with another key.
		{"login", func(*config.Config) { known.rev = "2" }},
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
		// The new agent knows the conversation of the one before.
		next()
		next()
		if built != i+2 {
			t.Fatalf("new %s: %d agents built for one configuration", tt.what, built)
		}
	}
	// All of that was the same access: what the LLM thought is still there.
	if access != first || thought() != "hm" {
		t.Fatalf("the last agent answers with %q, thoughts %q", access, thought())
	}
	// Another access is another provider: the conversation goes on there,
	// without what the LLM of the one before thought.
	cfg.Access = second
	next()
	if built != len(changes)+2 || access != second || thought() != "" {
		t.Fatalf("another access: %d agents, the last with %q, thoughts %q", built, access, thought())
	}
	if kept := a.memory.Conversation("thinking"); len(kept) != 2 || kept[1].Content != "ho" {
		t.Fatalf("the conversation did not stay: %+v", kept)
	}

	// The new agent is the one that is asked: it has the new model, name and tools.
	got, err := a.Ask(ctx, "other", "?system", nil)
	if err != nil || !strings.HasPrefix(got, `You are "you", the personal agent`) || !strings.Contains(got, ownerStyle+" Today is ") || strings.Contains(got, toolAskPeer) {
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
	// ...and the conversation goes on when the mistake is undone.
	cfg.Tools = &config.Tools{H2A: config.Named(toolListPeers)}
	next()
}

// The prompt of the owner's agent says how to use the tools it has, and what
// to make of a peer that asks for a file.
func TestOwnerPromptFollowsTheTools(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	file := agent.NewTool("edit", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	search := agent.NewTool(toolSearch, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	bash := agent.NewTool(ToolBash, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	rebuild := agent.NewTool(ToolRebuild, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM: overHTTP,
		Tools: func() []agent.Tool {
			return append(PeerTools(nil, func() string { return "" }), file, search, bash, rebuild)
		},
	}, discard)
	day := time.Date(2026, 10, 10, 23, 59, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return day }
	t.Cleanup(func() { now = old })
	start := `You are "me", the personal agent of the owner of this Ouroboros node. ` + scriptHint
	end := ownerStyle + " Today is Saturday, 10 October 2026; this machine runs " + runtime.GOOS + "/" + runtime.GOARCH + "."
	for _, tt := range []struct {
		tools []config.Entry
		want  string
	}{
		{config.Named(), start + end},
		{config.Named(toolListPeers), start + end},
		{config.Named(toolAskPeer), start + askHint + end},
		{config.Named("edit"), start + filesHint + workHint + end},
		{config.Named("search"), start + filesHint + workHint + end},
		{config.Named(ToolBash), start + workHint + end},
		{config.Named(toolAskPeer, ToolBash), start + askHint + workHint + claimHint + end},
		{config.Named(ToolRebuild), start + improveHint + end},
		{config.Named(ToolUserQuestion), start + questionHint + end},
		{config.Named(ToolDelegate), start + delegateHint + end},
		{config.Named("*"), start + askHint + filesHint + workHint + improveHint + claimHint + questionHint + delegateHint + end},
	} {
		cfg.Tools = &config.Tools{H2A: tt.tools}
		if got, err := a.Ask(context.Background(), "chat", "?system", nil); err != nil || got != tt.want {
			t.Errorf("with %s: %v\n got %q\nwant %q", tt.tools, err, got, tt.want)
		}
	}
	// The day the prompt names is the one it is: the agent is built anew
	// when it is over.
	day = day.Add(time.Minute)
	if got, err := a.Ask(context.Background(), "chat", "?system", nil); err != nil || !strings.Contains(got, " Today is Sunday, 11 October 2026;") {
		t.Errorf("the day after: %q, %v", got, err)
	}
	if strings.Join(fileTools, ",") != "read,write,edit,list,delete,search" {
		t.Errorf("the tools for files: %v", fileTools)
	}
}

// An agent with a workspace is told where it works, and that what the user
// works on is elsewhere; one without is told nothing of it.
func TestPromptNamesTheWorkspace(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	load := func() (config.Config, error) { return cfg, nil }
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }, Workspace: "/work/me"}
	want := " You work in /work/me, a directory of your own: your commands start there unless they name a directory." +
		" What the user works on is elsewhere: when they speak of a project and name no path, find it or ask, rather than looking for it there."
	if got, err := New(load, b, discard).Ask(context.Background(), "chat", "?system", nil); err != nil || !strings.HasSuffix(got, want) {
		t.Errorf("with a workspace: %q, %v", got, err)
	}
	b.Workspace = ""
	if got, err := New(load, b, discard).Ask(context.Background(), "chat", "?system", nil); err != nil || strings.Contains(got, "You work in") {
		t.Errorf("without a workspace: %q, %v", got, err)
	}
}

// An agent that may rebuild ouro is told where its source is, when the
// build knows it; one that may not is told nothing of it.
func TestPromptNamesTheSource(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	load := func() (config.Config, error) { return cfg, nil }
	rebuild := agent.NewTool(ToolRebuild, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return []agent.Tool{rebuild} }, Source: "/src/ouro"}
	want := improveHint + "Your source, the checkout rebuild builds, is /src/ouro. "
	if got, err := New(load, b, discard).Ask(context.Background(), "chat", "?system", nil); err != nil || !strings.Contains(got, want) {
		t.Errorf("with a source: %q, %v", got, err)
	}
	// An owner who wants changes of ouro proposed gets them proposed.
	cfg.SelfImprove = config.SelfImprovePropose
	if got, err := New(load, b, discard).Ask(context.Background(), "chat", "?system", nil); err != nil || !strings.Contains(got, proposeHint) || strings.Contains(got, improveHint) {
		t.Errorf("proposing: %q, %v", got, err)
	}
	cfg.SelfImprove = "ask"
	if err := New(load, b, discard).Ready(); err == nil || !strings.Contains(err.Error(), `self_improve is "build" or "propose", not "ask"`) {
		t.Errorf("a value of its own: %v", err)
	}
	cfg.SelfImprove = ""
	cfg.Tools = &config.Tools{H2A: config.Named()}
	if got, err := New(load, b, discard).Ask(context.Background(), "chat", "?system", nil); err != nil || strings.Contains(got, "/src/ouro") {
		t.Errorf("without rebuild: %q, %v", got, err)
	}
}

// ask_peer tells of answer_peer and tell_peer only where the agent has
// them too, for the owner and for peers: the LLM is not told of a tool it
// does not have.
func TestAskPeerNamesOnlyTheToolsThereAre(t *testing.T) {
	b := Builder{Tools: func() []agent.Tool { return PeerTools(nil, func() string { return "" }) }}
	answer, tell := " "+companions[0].text, " "+companions[1].text
	base := askPeer(nil, nil).Description
	for _, tt := range []struct {
		tools []config.Entry
		want  string
	}{
		{config.Named(toolAskPeer), base},
		{config.Named(toolAskPeer, toolAnswer), base + answer},
		{config.Named(toolAskPeer, toolTellPeer), base + tell},
		{config.Named("*"), base + answer + tell},
	} {
		cfg := config.Config{Tools: &config.Tools{H2A: tt.tools, A2A: tt.tools}}
		owner, err := b.ownerTools(cfg)
		if err != nil {
			t.Fatal(err)
		}
		peer, err := b.peerTools(cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, tools := range [][]agent.Tool{owner, peer} {
			if i := slices.IndexFunc(tools, func(t agent.Tool) bool { return t.Name == toolAskPeer }); tools[i].Description != tt.want {
				t.Errorf("with %s: %q", tt.tools, tools[i].Description)
			}
		}
	}
	if tools, err := b.ownerTools(config.Config{Tools: &config.Tools{H2A: config.Named(toolAnswer)}}); err != nil || len(tools) != 1 ||
		tools[0].Description != answerPeer(nil).Description {
		t.Fatalf("without ask_peer: %v, %v", tools, err)
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

	// A peer named by its ID is shown by the name the owner knows it by.
	nameOf := func(peer string) string {
		if peer == "f0217e5a032d" {
			return "luna"
		}
		return ""
	}
	for peer, want := range map[string]string{"f0217e5a032d": "asking luna", "beta": "asking beta"} {
		if got := AskingPeerAs(nameOf)(args(askPeerArgs{Peer: peer})); got != want {
			t.Errorf("%s: AskingPeerAs = %q, want %q", peer, got, want)
		}
	}
	if got := AskingPeerAs(nameOf)(nil); got != "" {
		t.Errorf("no arguments: AskingPeerAs = %q", got)
	}
}

// Once a call ended, the owner is shown how long it took, or the first line
// of why it failed, cut where a character begins.
func TestHowItEnded(t *testing.T) {
	long := strings.Repeat("a", 119) + "äb"
	tests := []struct {
		name string
		step Step
		want string
	}{
		{"running", Step{Took: time.Second}, ""},
		{"done", Step{Done: true, Took: 1234567 * time.Microsecond}, "✓ 1.235s"},
		{"failed", Step{Done: true, Err: errors.New("no node \"luna\"\nsee: ouro list")}, `✗ no node "luna"`},
		{"empty first line", Step{Done: true, Err: errors.New("\nsee: ouro list")}, "✗ "},
		{"carriage return", Step{Done: true, Err: errors.New("a\rb")}, `✗ a\rb`},
		{"120 bytes", Step{Done: true, Err: errors.New(strings.Repeat("a", 120))}, "✗ " + strings.Repeat("a", 120)},
		{"cut in a character", Step{Done: true, Err: errors.New(long)}, "✗ " + strings.Repeat("a", 119) + "…"},
		{"cut after a character", Step{Done: true, Err: errors.New("ä" + long)}, "✗ ä" + strings.Repeat("a", 118) + "…"},
		{"remark", Step{Done: true, Took: time.Second, Remark: long + "\nmore"}, "✓ 1s · " + long},
		{"remark of a failed call", Step{Done: true, Err: errors.New("no"), Remark: "said"}, "✗ no"},
	}
	for _, tt := range tests {
		if got := HowItEnded(tt.step); got != tt.want {
			t.Errorf("%s: HowItEnded = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// discard is a log that is not kept.
func discard(string, ...any) {}
