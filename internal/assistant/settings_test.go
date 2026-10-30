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

// Configuration changes ask the owner directly and persist only an explicit approval.
func TestProposeConfig(t *testing.T) {
	for _, reply := range []string{"Apply change", "Reject change", "yes", "", "Apply change please"} {
		t.Run(reply, func(t *testing.T) {
			c := newConfigured(t)
			asked := false
			ctx := agent.WithOwner(context.Background(), func(_ context.Context, raw json.RawMessage) (string, error) {
				q, err := ParseQuestion(raw)
				if err != nil || !strings.Contains(q.Question, `name from "me" to "you"`) || !strings.Contains(q.Question, "a better name") || q.Recommended != "Reject change" {
					t.Fatalf("question: %+v, %v", q, err)
				}
				asked = true
				if c.config().Name != "me" || len(c.waiting()) != 0 {
					t.Fatal("changed or queued before approval")
				}
				return reply, nil
			})
			got, err := c.builder.proposeConfig().Run(ctx, json.RawMessage(`{"path":"name","value":"you","reason":"a better name"}`))
			want := "me"
			if reply == "Apply change" {
				want = "you"
			}
			if err != nil || !asked || c.config().Name != want || len(c.waiting()) != 0 {
				t.Fatalf("result %q, %v; name %q", got, err, c.config().Name)
			}
		})
	}
	// Lists encoded as strings are normalized before asking and saving.
	c := newConfigured(t)
	ctx := agent.WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) { return "Apply change", nil })
	got, err := c.builder.proposeConfig().Run(ctx, json.RawMessage(`{"path":"tools.protected","value":"[\"~/.ssh\",\"~/secret\"]","reason":"more"}`))
	if err != nil || !strings.Contains(got, `["~/.ssh","~/secret"]`) || len(c.config().Protected()) != 2 || len(c.waiting()) != 0 {
		t.Fatalf("list: %q, %v", got, err)
	}
}

func TestProposeConfigDoesNotApplyWithoutAuthorization(t *testing.T) {
	for _, mode := range []string{"no owner", "timeout", "cancelled reply", "stale", "unrelated edit", "invalid latest config"} {
		t.Run(mode, func(t *testing.T) {
			c := newConfigured(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode != "no owner" {
				ctx = agent.WithOwner(ctx, func(context.Context, json.RawMessage) (string, error) {
					switch mode {
					case "timeout":
						return "Apply change", context.DeadlineExceeded
					case "cancelled reply":
						cancel()
					case "stale", "unrelated edit", "invalid latest config":
						cfg := c.config()
						if mode == "stale" {
							cfg.Name = "someone else"
						} else if mode == "invalid latest config" {
							cfg.Tools = &config.Tools{A2A: config.Named("unknown")}
						} else {
							cfg.Model = "new-model"
						}
						c.save(cfg)
					}
					return "Apply change", nil
				})
			}
			_, err := c.builder.proposeConfig().Run(ctx, json.RawMessage(`{"path":"name","value":"you","reason":"r"}`))
			want := "me"
			if mode == "stale" {
				want = "someone else"
			} else if mode == "unrelated edit" {
				want = "you"
				if c.config().Model != "new-model" {
					t.Fatal("unrelated edit lost")
				}
			}
			if (err == nil) != (mode == "unrelated edit") || c.config().Name != want || len(c.waiting()) != 0 {
				t.Fatalf("err %v, name %q, waiting %+v", err, c.config().Name, c.waiting())
			}
		})
	}
}

// How rebuild builds is no field of the configuration, and neither is a
// sandbox of bash, which runs in none.
func TestProposeSwitch(t *testing.T) {
	c := newConfigured(t)
	for _, path := range []string{"rebuild_without_sandbox", "rebuild_without_tests", "bash_without_sandbox"} {
		if got := c.tool(toolProposeConfig, propose{path, true, "r"}); !strings.HasPrefix(got, `error: nothing was proposed: the configuration has no field "`+path+`" that can be proposed`) {
			t.Fatalf("%s: %q", path, got)
		}
	}
	if list := c.waiting(); len(list) != 0 {
		t.Fatalf("waiting %+v", list)
	}
	if got := c.tool(toolGetConfig, struct{}{}); strings.Contains(got, "rebuild_") || strings.Contains(got, "sandbox") {
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
	if _, err := c.proposals.Add(c.config(), "name", json.RawMessage(`"you"`), "a better name"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.proposals.Add(c.config(), "tools.a2a", json.RawMessage(`["read"]`), "peers may read"); err != nil {
		t.Fatal(err)
	}
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
		{propose{"tools.h2a", []string{"nope"}, "r"}, `error: nothing was proposed: tools.h2a: unknown tool "nope" (there are: list_peers, ask_peer, answer_peer, tell_peer, ask_back, read, user_question, delegate, get_config, propose_config, withdraw_proposal)`},
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

	// Valid changes ask directly, and never occupy the pending store.
	ctx := agent.WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) {
		return "Apply change", nil
	})
	for _, args := range []propose{
		{"tools.h2a", []string{"read", toolGetConfig, toolProposeConfig}, "r"},
		{"tools.a2a", []string{"*"}, "r"},
		{"tools.protected", []string{}, "r"},
		{"model", "gpt-4o-mini", "r"},
	} {
		raw, _ := json.Marshal(args)
		if got, err := c.builder.proposeConfig().Run(ctx, raw); err != nil || !strings.HasPrefix(got, "Configuration changed") {
			t.Errorf("%+v: %q, %v", args, got, err)
		}
	}
	if len(c.waiting()) != 0 {
		t.Fatal("direct changes left pending proposals")
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
	tools, err := c.builder.peerTools(cfg, "")
	if err != nil || names(tools) != "list_peers,ask_peer,answer_peer,tell_peer,ask_back,read" {
		t.Fatalf("every tool for peers: %q, %v", names(tools), err)
	}
	tools, err = c.builder.ownerTools(cfg)
	if err != nil || names(tools) != "list_peers,ask_peer,answer_peer,tell_peer,ask_back,read,user_question,delegate,get_config,propose_config,withdraw_proposal" {
		t.Fatalf("every tool for the owner: %q, %v", names(tools), err)
	}
	// Configuration tools are not advertised to peers, even with every tool allowed.
	b := c.builder
	b.LLM = func(string) (agent.LLM, error) { return promptLLM{}, nil }
	bot, err := b.ForPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bot.Respond(from("luna"), "c", "tools")
	if err != nil || !strings.HasSuffix(got, "tools: list_peers,ask_peer,answer_peer,tell_peer,ask_back,read") {
		t.Fatalf("peer direct tools: %q, %v", got, err)
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
	// the owner and the one that hands tasks to helpers.
	if got := names((Builder{Tools: func() []agent.Tool { return nil }}).all()); got != ToolUserQuestion+","+ToolDelegate {
		t.Fatalf("without a node: %q", got)
	}
}
