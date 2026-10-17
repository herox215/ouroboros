package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

// directNode is a node whose agents have run and two tools to call from it:
// who says whom the conversation is with, as far as the hops, and note takes
// the setting "prefix".
func directNode(t *testing.T) (config.Config, Builder) {
	t.Helper()
	who := agent.NewTool("who", "", nil, func(ctx context.Context, _ struct{}) (string, error) {
		return fmt.Sprintf("%s in %s", ctx.Value(asker{}), agent.ContextID(ctx)), nil
	})
	type settings struct {
		Prefix string `json:"prefix"`
	}
	type noteArgs struct {
		Text string `json:"text"`
	}
	note := agent.Configurable(agent.Tool{Name: "note", Schema: `{"type":"object"}`}, func(s settings) (agent.RunFunc, error) {
		return func(_ context.Context, raw json.RawMessage) (string, error) {
			var args noteArgs
			if json.Unmarshal(raw, &args) != nil || args.Text == "" {
				return "", errTest
			}
			return s.Prefix + args.Text, nil
		}, nil
	})
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	return cfg, Builder{LLM: overHTTP, Tools: func() []agent.Tool { return []agent.Tool{who, note} }}
}

// asker is the key under which a test says who asks.
type asker struct{}

// result is what the fake LLM says after a tool ran: its result.
func result(t *testing.T, got string, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	_, last, ok := strings.Cut(got, " last=")
	if !ok {
		t.Fatalf("no tool ran: %q", got)
	}
	return last
}
func TestDirectCallsTellWhatTheLLMSaysAlongWithTools(t *testing.T) {
	llm := &toolLLM{saying: "let me look"}
	a, _ := recorded(t, ownerConfig(), llm)
	var said []string
	say := func(text string) { said = append(said, text) }
	if _, err := a.Run(context.Background(), "c", `!echo {"text":"x"}`, nil, say); err != nil {
		t.Fatal(err)
	}
	if len(said) != 1 || said[0] != "let me look" {
		t.Fatalf("with a call of a tool: %q", said)
	}
	// An answer in text alone is the answer: it is not said on the way.
	if _, err := a.Run(context.Background(), "c", "hello", nil, say); err != nil || len(said) != 1 {
		t.Fatalf("with an answer alone: %q, %v", said, err)
	}
	llm.failing = errTest
	if _, err := a.Run(context.Background(), "c", `!echo {"text":"x"}`, nil, say); err == nil || len(said) != 1 {
		t.Fatalf("with a call that failed: %q, %v", said, err)
	}
}

// The first line of a text ends at its first line break, also when that is
// where the text begins.
func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{"one\ntwo": "one", "\nrest": "", "": "", "plain": "plain"} {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each audience sees its allowed tools themselves, never a script executor.
func TestDirectToolInventories(t *testing.T) {
	cfg, b := directNode(t)
	b.LLM = func(string) (agent.LLM, error) { return promptLLM{}, nil }
	cfg.Tools = &config.Tools{H2A: config.Named("who", "note"), A2A: config.Named("note")}
	owner, err := b.forOwner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := b.ForPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := b.ownerTools(cfg)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := b.helper(cfg, tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, want string
		bot        *agent.Agent
	}{
		{"owner", "tools: who,note", owner}, {"peer", "tools: note", peer}, {"helper", "tools: who,note", helper},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.bot.Respond(context.Background(), "c", "inventory")
			if err != nil || !strings.HasSuffix(got, tt.want) || strings.Contains(got, "You act through run") {
				t.Fatalf("%q, %v", got, err)
			}
		})
	}
	// Obsolete run allowlist entries fail as unknown tools, not a hidden capability.
	cfg.Tools.A2A = config.Named("run")
	if _, err := b.ForPeers(cfg); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("obsolete tool: %v", err)
	}
}

// Direct calls preserve settings, origin and conversation, report one start/end
// pair with a stable call ID, and deliver tool failures to the model.
func TestDirectOwnerCalls(t *testing.T) {
	cfg, b := directNode(t)
	cfg.Tools = &config.Tools{H2A: []config.Entry{{Tool: "who"}, {Tool: "note", Settings: json.RawMessage("{\"prefix\":\"set \"}")}}}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	ctx := context.WithValue(context.Background(), asker{}, "owner")
	for _, tt := range []struct{ message, want, tool string }{
		{"!who {}", "owner in c", "who"},
		{"!note {\"text\":\"a\"}", "set a", "note"},
		{"!note {}", "error: test error", "note"},
	} {
		var steps []Step
		got, err := a.Ask(ctx, "c", tt.message, func(s Step) { steps = append(steps, s) })
		if result(t, got, err) != tt.want {
			t.Fatalf("%s: %q", tt.message, got)
		}
		if len(steps) != 2 || steps[0].Tool != tt.tool || steps[0].Done || !steps[1].Done || steps[0].ID == "" || steps[1].ID != steps[0].ID {
			t.Fatalf("direct steps: %+v", steps)
		}
	}
}

func TestDirectPeerCalls(t *testing.T) {
	cfg, b := directNode(t)
	cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "who"}, {Tool: "note", Settings: json.RawMessage("{\"prefix\":\"peer: \"}")}}}
	bot, err := b.ForPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(from("luna"), asker{}, "luna")
	for _, tt := range []struct{ message, want string }{
		{"!who {}", "luna in c"}, {"!note {\"text\":\"a\"}", "peer: a"}, {"!note {}", "error: test error"},
	} {
		got, err := bot.Respond(ctx, "c", tt.message)
		if result(t, got, err) != tt.want {
			t.Fatalf("%s: %q", tt.message, got)
		}
	}
}

func TestDirectFailuresCountOnce(t *testing.T) {
	var m measures
	for range 3 {
		m.step("read", "{}", time.Second, true)
	}
	if m.failed != 3 {
		t.Fatalf("failed: %d", m.failed)
	}
	signals := m.signals(0, 0, 0, 0, "ok", nil)
	if len(signals) != 2 || signals[0].Kind != SignalFailedTools || signals[1].Kind != SignalRepeated {
		t.Fatalf("signals: %+v", signals)
	}
}

// A model cannot bypass an audience's allowlist by calling a hidden tool.
func TestDirectCallsCannotReachUnlistedTools(t *testing.T) {
	cfg, b := directNode(t)
	b.LLM = func(string) (agent.LLM, error) { return &toolLLM{}, nil }
	cfg.Tools = &config.Tools{A2A: config.Named("note")}
	peer, err := b.ForPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	note, err := b.peerTools(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	helper, err := b.helper(cfg, note)
	if err != nil {
		t.Fatal(err)
	}
	for _, bot := range []*agent.Agent{peer, helper} {
		for _, tool := range []string{"who", "run", ToolUserQuestion, toolProposeConfig} {
			got, err := bot.Respond(context.Background(), "c", "!"+tool+" {}")
			if err != nil || !strings.Contains(got, "error:") || !strings.Contains(got, tool) {
				t.Fatalf("unlisted %s: %q, %v", tool, got, err)
			}
		}
	}
}
