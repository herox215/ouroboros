package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/proposal"
)

// configured is a node whose owner's agent sees the configuration and
// proposes changes of it.
type configured struct {
	t         *testing.T
	dir       string
	failing   error // what reading the configuration fails with
	proposals *proposal.Store
	builder   Builder
	assistant *Assistant
}

func newConfigured(t *testing.T) *configured {
	t.Helper()
	c := &configured{t: t, dir: t.TempDir()}
	c.proposals = proposal.Open(filepath.Join(c.dir, "proposals.json"))
	read := func() (config.Config, error) {
		cfg, err := config.Load(c.dir)
		if c.failing != nil {
			err = c.failing
		}
		return cfg, err
	}
	type settings struct {
		Allow []string `json:"allow"`
	}
	read1 := agent.Configurable(agent.Tool{Name: "read", Schema: `{"type":"object"}`}, func(settings) (agent.RunFunc, error) {
		return func(context.Context, json.RawMessage) (string, error) { return "content", nil }, nil
	})
	c.builder = Builder{LLM: overHTTP, Tools: func() []agent.Tool { return append(PeerTools(nil, func() string { return "" }), read1) }}.
		WithConfig(read, c.proposals)
	c.assistant = New(read, c.builder, discard)
	c.save(config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()})
	return c
}

func (c *configured) save(cfg config.Config) {
	c.t.Helper()
	if err := config.Save(c.dir, cfg); err != nil {
		c.t.Fatal(err)
	}
}

func (c *configured) config() config.Config {
	c.t.Helper()
	cfg, err := config.Load(c.dir)
	if err != nil {
		c.t.Fatal(err)
	}
	return cfg
}

// tool makes the owner's agent call a tool, and returns what the LLM got.
func (c *configured) tool(name string, args any) string {
	c.t.Helper()
	raw, _ := json.Marshal(args)
	got, err := c.assistant.Ask(context.Background(), "chat", "!"+name+" "+string(raw), nil)
	return result(c.t, got, err)
}

// waiting are the proposals that wait.
func (c *configured) waiting() []proposal.Proposal {
	c.t.Helper()
	list, err := c.proposals.List()
	if err != nil {
		c.t.Fatal(err)
	}
	return list
}

type propose struct {
	Path   string `json:"path,omitempty"`
	Value  any    `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// The agent of the owner sees the configuration without its secrets.
func TestGetConfig(t *testing.T) {
	c := newConfigured(t)
	got := c.tool(toolGetConfig, struct{}{})
	cfg := c.config()
	if got != string(cfg.Public("")) || strings.Contains(got, cfg.Access) || strings.Contains(got, cfg.PrivateKey) || !strings.Contains(got, `"name": "me"`) {
		t.Fatalf("got %s", got)
	}
	// With the accesses of the node, it names the one that answers as the
	// owner sees it: not its ID, and nothing of what it signs in with.
	named := c.builder
	named.Accesses = &accesses{label: "Ollama local", secrets: []string{"sk-secret"}}
	shown, err := named.getConfig().Run(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(shown, `"provider": "Ollama local"`) || strings.Contains(shown, "sk-secret") || strings.Contains(shown, cfg.Access) {
		t.Fatalf("with accesses: %s, %v", shown, err)
	}
	// As it is now, not as it was when the agent was built.
	cfg.Tools = &config.Tools{A2A: config.Named("list_peers")}
	c.save(cfg)
	if got := c.tool(toolGetConfig, struct{}{}); !strings.Contains(got, `"list_peers"`) {
		t.Fatalf("after a change: %s", got)
	}
	c.failing = errTest
	if _, err := c.builder.getConfig().Run(context.Background(), json.RawMessage(`{}`)); err != errTest {
		t.Fatalf("a configuration that cannot be read: %v", err)
	}
}

// A proposal changes nothing: it waits for the owner, and the agent is told
// so.
func TestProposeConfig(t *testing.T) {
	c := newConfigured(t)
	got := c.tool(toolProposeConfig, propose{"tools.a2a", []any{"list_peers", map[string]any{"tool": "read", "allow": []string{"~/share"}}}, "so that peers can read the share"})
	want := `Proposed, and nothing is changed yet: tools.a2a is to become ["list_peers",{"allow":["~/share"],"tool":"read"}] instead of []. ` +
		"The proposal waits for your owner among the requests in the terminal UI (the command /pending), where they accept or refuse it. " +
		"Tell them that, and do not say that the change is made. Its id is "
	if !strings.HasPrefix(got, want) {
		t.Fatalf("got %q\nwant %q…", got, want)
	}
	if c.config().Tools != nil {
		t.Fatal("the proposal changed the configuration")
	}
	list := c.waiting()
	if len(list) != 1 || list[0].Path != "tools.a2a" || string(list[0].Value) != `["list_peers",{"allow":["~/share"],"tool":"read"}]` ||
		string(list[0].Before) != `[]` || list[0].Reason != "so that peers can read the share" {
		t.Fatalf("waiting: %+v", list)
	}
	// The agent is told the id, so that it can take the proposal back; how,
	// the prompt tells where it may.
	if end := strings.TrimPrefix(got, want); end != list[0].ID+"." {
		t.Fatalf("the id in the answer: %q", end)
	}

	// A list that is handed over as a text is the list.
	got = c.tool(toolProposeConfig, propose{"tools.protected", `["~/.ssh", "~/secret"]`, "more"})
	if !strings.HasPrefix(got, `Proposed, and nothing is changed yet: tools.protected is to become ["~/.ssh","~/secret"] instead of ["~/.ssh",`) {
		t.Fatalf("a list as text: %q", got)
	}
	got = c.tool(toolProposeConfig, propose{"name", "you", "a better name"})
	if !strings.HasPrefix(got, `Proposed, and nothing is changed yet: name is to become "you" instead of "me".`) {
		t.Fatalf("a name: %q", got)
	}
	got = c.tool(toolProposeConfig, propose{"description", "knows the way", "say what I do"})
	if !strings.HasPrefix(got, `Proposed, and nothing is changed yet: description is to become "knows the way" instead of "Ouroboros instance named me".`) {
		t.Fatalf("a description: %q", got)
	}
	if got := c.tool(toolProposeConfig, propose{"description", "two\nlines", "r"}); !strings.HasPrefix(got, "error: nothing was proposed: description is one line") {
		t.Fatalf("a description of two lines: %q", got)
	}
	if len(c.waiting()) != 4 || c.config().Name != "me" || c.config().Description != "" {
		t.Fatalf("waiting: %+v", c.waiting())
	}
}

// How rebuild builds is no field of the configuration, and the sandbox of
// bash is no proposal.
func TestProposeSwitch(t *testing.T) {
	c := newConfigured(t)
	for _, path := range []string{"rebuild_without_sandbox", "rebuild_without_tests"} {
		if got := c.tool(toolProposeConfig, propose{path, true, "r"}); !strings.HasPrefix(got, `error: nothing was proposed: the configuration has no field "`+path+`" that can be proposed`) {
			t.Fatalf("%s: %q", path, got)
		}
	}
	if got := c.tool(toolProposeConfig, propose{"bash_without_sandbox", true, "r"}); !strings.HasPrefix(got, "error: nothing was proposed: bash_without_sandbox is set by the owner alone") {
		t.Fatalf("bash: %q", got)
	}
	if list := c.waiting(); len(list) != 0 {
		t.Fatalf("waiting %+v", list)
	}
	if got := c.tool(toolGetConfig, struct{}{}); strings.Contains(got, "rebuild") || !strings.Contains(got, `"bash_without_sandbox": false`) {
		t.Fatalf("shown: %s", got)
	}
}

// withdraw are the arguments of the tool that takes a proposal back.
type withdraw struct {
	ID string `json:"id,omitempty"`
}

// The agent takes a proposal of its own back: nothing is changed, and the
// owner has one decision less to make.
func TestWithdrawProposal(t *testing.T) {
	c := newConfigured(t)
	if got := c.tool(toolWithdrawConfig, struct{}{}); got != "No proposal waits for your owner: there is nothing of yours to take back." {
		t.Fatalf("nothing waits: %q", got)
	}
	// Called without any arguments at all, it lists what waits too.
	if got, err := c.builder.withdrawProposal().Run(context.Background(), nil); err != nil || !strings.HasPrefix(got, "No proposal waits") {
		t.Fatalf("without arguments: %q, %v", got, err)
	}

	// What waits is listed with the id it is taken back with.
	c.tool(toolProposeConfig, propose{"name", "you", "a better name"})
	c.tool(toolProposeConfig, propose{"tools.a2a", []string{"read"}, "peers may read"})
	list := c.waiting()
	if len(list) != 2 {
		t.Fatalf("waiting: %+v", list)
	}
	want := fmt.Sprintf("2 of your proposals wait for your owner:\n%s: name is to become \"you\" instead of \"me\" (a better name)\n"+
		"%s: tools.a2a is to become [\"read\"] instead of [] (peers may read)\n"+
		"Take one back with %s, giving its id.", list[0].ID, list[1].ID, toolWithdrawConfig)
	if got := c.tool(toolWithdrawConfig, struct{}{}); got != want {
		t.Fatalf("what waits:\n got %q\nwant %q", got, want)
	}

	// One of them is taken back, and the configuration is as it was.
	if got := c.tool(toolWithdrawConfig, withdraw{list[0].ID}); !strings.HasPrefix(got, "Taken back, and nothing is changed: the proposal for name no longer waits for your owner") {
		t.Fatalf("taken back: %q", got)
	}
	if waiting := c.waiting(); len(waiting) != 1 || waiting[0].ID != list[1].ID {
		t.Fatalf("after taking one back: %+v", c.waiting())
	}
	if cfg := c.config(); cfg.Name != "me" || cfg.Tools != nil {
		t.Fatalf("the configuration changed: %+v", cfg)
	}

	// An id that does not wait is an error, and says how to see what waits.
	gone := `error: no proposal waits with the id "` + list[0].ID + `": withdraw_proposal without an id lists what waits`
	if got := c.tool(toolWithdrawConfig, withdraw{list[0].ID}); got != gone {
		t.Fatalf("an id that is gone: %q", got)
	}

	// What the owner decided on is out of reach.
	if _, err := c.proposals.Accept(c.dir, list[1].ID); err != nil {
		t.Fatal(err)
	}
	if got := c.tool(toolWithdrawConfig, withdraw{list[1].ID}); !strings.HasPrefix(got, "error: no proposal waits with the id ") {
		t.Fatalf("an accepted proposal: %q", got)
	}
	if len(c.waiting()) != 0 || c.config().Tools == nil {
		t.Fatalf("the owner's decision: %+v", c.waiting())
	}

	// What is no id is said so.
	if got := c.tool(toolWithdrawConfig, "x"); got != `error: withdraw_proposal takes an "id", or nothing at all to list what waits` {
		t.Fatalf("no id: %q", got)
	}
	// A list that cannot be read takes nothing back, and shows nothing.
	if err := os.WriteFile(filepath.Join(c.dir, "proposals.json"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.tool(toolWithdrawConfig, withdraw{"x"}); !strings.HasPrefix(got, "error: nothing was taken back: ") {
		t.Fatalf("an unreadable list: %q", got)
	}
	if got := c.tool(toolWithdrawConfig, struct{}{}); !strings.HasPrefix(got, "error: ") || strings.Contains(got, "wait") {
		t.Fatalf("an unreadable list: %q", got)
	}
}

// What the node would not start with is an error at once, and no proposal.
func TestProposeConfigChecksTheValue(t *testing.T) {
	c := newConfigured(t)
	for _, tt := range []struct {
		args propose
		want string
	}{
		{propose{"tools.a2a", []string{"ask_peers"}, "r"}, `error: nothing was proposed: tools.a2a: unknown tool "ask_peers" (there are: list_peers, ask_peer, answer_peer, tell_peer, ask_back, read)`},
		{propose{"tools.h2a", []string{"nope"}, "r"}, `error: nothing was proposed: tools.h2a: unknown tool "nope" (there are: list_peers, ask_peer, answer_peer, tell_peer, ask_back, read, user_question, get_config, propose_config, withdraw_proposal)`},
		{propose{"tools.a2a", []any{map[string]any{"tool": "read", "alow": []string{"~"}}}, "r"}, `error: nothing was proposed: tools.a2a: settings of the tool read: json: unknown field "alow"`},
		{propose{"tools.a2a", []any{map[string]any{"tool": "list_peers", "allow": []string{"~"}}}, "r"}, `error: nothing was proposed: tools.a2a: the tool list_peers takes no settings`},
		{propose{"tools.a2a", []string{"read", "read"}, "r"}, `error: nothing was proposed: tools.a2a: the tool "read" is listed twice`},
		{propose{"tools.a2a", []string{toolGetConfig}, "r"}, `error: nothing was proposed: tools.a2a: the tool get_config is for the owner of the node only: it is no tool for peers`},
		{propose{"tools.a2a", []string{toolProposeConfig}, "r"}, `error: nothing was proposed: tools.a2a: the tool propose_config is for the owner of the node only: it is no tool for peers`},
		{propose{"tools.a2a", "read", "r"}, `error: nothing was proposed: tools.a2a: json: cannot unmarshal string into Go value of type []config.Entry`},
		{propose{"tools.a2a", `["read"`, "r"}, `error: nothing was proposed: tools.a2a: json: cannot unmarshal string into Go value of type []config.Entry`},
		{propose{"tools.protected", []string{"secrets"}, "r"}, `error: nothing was proposed: tools.protected: the path "secrets" has to be absolute or start with ~`},
		{propose{"name", 3, "r"}, `error: nothing was proposed: name: json: cannot unmarshal number into Go value of type string`},
		{propose{"name", "me", "r"}, `error: nothing was proposed: name is that already: there is nothing to change`},
		{propose{"api_key", "sk-mine", "r"}, `error: nothing was proposed: api_key is changed by the owner alone, with ouro start --setup: no proposal changes it`},
		{propose{"private_key", "mine", "r"}, `error: nothing was proposed: private_key is changed by the owner alone, with ouro start --setup: no proposal changes it`},
		{propose{"base_url", "http://evil.example/v1", "r"}, `error: nothing was proposed: base_url is changed by the owner alone, with ouro start --setup: no proposal changes it`},
		{propose{"tools", map[string]any{}, "r"}, `error: nothing was proposed: the configuration has no field "tools" that can be proposed: there are name, description, model, tools.h2a, tools.a2a, tools.protected`},
		{propose{"", "x", "r"}, `error: propose_config needs "path", "value" and "reason"`},
		{propose{"name", nil, "r"}, `error: propose_config needs "path", "value" and "reason"`},
		{propose{"name", "x", ""}, `error: propose_config needs "path", "value" and "reason"`},
	} {
		if got := c.tool(toolProposeConfig, tt.args); got != tt.want {
			t.Errorf("%+v:\n got %q\nwant %q", tt.args, got, tt.want)
		}
	}
	got, err := c.assistant.Ask(context.Background(), "chat", "!"+toolProposeConfig+` "name"`, nil)
	if got := result(t, got, err); got != `error: propose_config needs "path", "value" and "reason"` {
		t.Errorf("arguments that are no object: %q", got)
	}
	if list := c.waiting(); len(list) != 0 {
		t.Fatalf("waiting: %+v", list)
	}

	// What is allowed for the owner is a proposal: every tool, and "*" for peers.
	for _, args := range []propose{
		{"tools.h2a", []string{"read", toolGetConfig, toolProposeConfig}, "r"},
		{"tools.a2a", []string{"*"}, "r"},
		{"tools.protected", []string{}, "r"},
		{"model", "gpt-4o-mini", "r"},
	} {
		if got := c.tool(toolProposeConfig, args); !strings.HasPrefix(got, "Proposed, and nothing is changed yet: ") {
			t.Errorf("%+v: %q", args, got)
		}
	}

	// No more wait than fit, and the agent is told to whom to turn.
	for i := len(c.waiting()); i < proposal.Max; i++ {
		c.tool(toolProposeConfig, propose{"name", strings.Repeat("n", i+3), "r"})
	}
	if got := c.tool(toolProposeConfig, propose{"name", "one more", "r"}); got != "error: 16 proposals are waiting for your owner already, and no more are kept: "+
		"they decide on them among the requests in the terminal UI (/pending)" {
		t.Errorf("a full list: %q", got)
	}
	if len(c.waiting()) != proposal.Max {
		t.Fatalf("%d waiting", len(c.waiting()))
	}

	c.failing = errTest
	if _, err := c.builder.proposeConfig().Run(context.Background(), json.RawMessage(`{"path": "name", "value": "x", "reason": "r"}`)); err == nil || err.Error() != "nothing was proposed: test error" {
		t.Fatalf("a configuration that cannot be read: %v", err)
	}
}

// Both tools are for the owner alone: a peer has neither, whatever a2a
// lists, and listing them there is an error.
func TestConfigToolsAreForTheOwner(t *testing.T) {
	c := newConfigured(t)
	for _, tool := range c.builder.configTools() {
		got, err := tool.Run(from("luna"), json.RawMessage(`{"path": "name", "value": "luna's", "reason": "because"}`))
		if err == nil || got != "" || err.Error() != tool.Name+" is for the owner of this node only" || !tool.OwnerOnly {
			t.Errorf("%s for a peer: %q, %v", tool.Name, got, err)
		}
	}
	if len(c.waiting()) != 0 {
		t.Fatal("a peer proposed a change")
	}

	cfg := c.config()
	for _, name := range []string{toolGetConfig, toolProposeConfig, toolWithdrawConfig} {
		cfg.Tools = &config.Tools{A2A: config.Named("read", name)}
		if _, err := c.builder.ForPeers(cfg); err == nil || err.Error() != "tools.a2a: the tool "+name+" is for the owner of the node only: it is no tool for peers" {
			t.Errorf("%s in a2a: %v", name, err)
		}
		if err := c.builder.Check(cfg); err == nil || !strings.HasPrefix(err.Error(), "tools.a2a: ") {
			t.Errorf("%s in a2a: %v", name, err)
		}
		// For the owner it is a tool like any other.
		cfg.Tools = &config.Tools{H2A: config.Named(name)}
		if err := c.builder.Check(cfg); err != nil {
			t.Errorf("%s in h2a: %v", name, err)
		}
	}
	// A description written by hand is checked like a proposed one.
	cfg.Description = "two\nlines"
	if err := c.builder.Check(cfg); err == nil || !strings.HasPrefix(err.Error(), "description is one line") {
		t.Errorf("a description of two lines: %v", err)
	}
	cfg.Description = ""
	// Every tool for peers is every tool there is for peers.
	cfg.Tools = &config.Tools{A2A: config.Named("*")}
	tools, err := c.builder.peerTools(cfg)
	if err != nil || names(tools) != "list_peers,ask_peer,answer_peer,tell_peer,ask_back,read" {
		t.Fatalf("every tool for peers: %q, %v", names(tools), err)
	}
	tools, err = c.builder.ownerTools(cfg)
	if err != nil || names(tools) != "list_peers,ask_peer,answer_peer,tell_peer,ask_back,read,user_question,get_config,propose_config,withdraw_proposal" {
		t.Fatalf("every tool for the owner: %q, %v", names(tools), err)
	}
	// A script of a peer has neither.
	bot, err := c.builder.ForPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bot.Respond(from("luna"), "c", runs(`return typeof get_config + " " + typeof propose_config + " " + typeof withdraw_proposal + " " + typeof read`))
	if got := result(t, got, err); got != "undefined undefined undefined function" {
		t.Fatalf("a script of a peer: %q", got)
	}

	// The owner's agent is told what a proposal is, and that it may take its
	// own back, as far as it has the tools for that.
	prompt, err := c.assistant.Ask(context.Background(), "p", "?system", nil)
	if err != nil || !strings.Contains(prompt, configHint+withdrawHint) {
		t.Fatalf("the prompt: %q, %v", prompt, err)
	}
	cfg.Tools = &config.Tools{H2A: config.Named(toolProposeConfig)}
	c.save(cfg)
	if prompt, err := c.assistant.Ask(context.Background(), "p", "?system", nil); err != nil || !strings.Contains(prompt, configHint) ||
		strings.Contains(prompt, toolWithdrawConfig) {
		t.Fatalf("the prompt without %s: %q, %v", toolWithdrawConfig, prompt, err)
	}
	cfg.Tools = &config.Tools{H2A: config.Named(toolGetConfig)}
	c.save(cfg)
	if prompt, err := c.assistant.Ask(context.Background(), "p", "?system", nil); err != nil || strings.Contains(prompt, configHint) {
		t.Fatalf("the prompt without the tool: %q, %v", prompt, err)
	}

	// A blueprint without a node has neither tool: only the one that asks
	// the owner.
	if got := names((Builder{Tools: func() []agent.Tool { return nil }}).all()); got != ToolUserQuestion {
		t.Fatalf("without a node: %q", got)
	}
}
