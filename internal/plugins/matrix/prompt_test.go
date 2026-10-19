package matrix

import (
	"context"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

func TestOutputHookScope(t *testing.T) {
	matrix := openai.Message{Role: "user", Source: openai.MessageSource{Interface: "api", Gateway: "matrix"}}
	for _, tc := range []struct {
		name     string
		messages []openai.Message
		peer     bool
		want     int
	}{
		{"matrix", []openai.Message{matrix}, false, 1},
		{"gui after matrix", []openai.Message{matrix, {Role: "user", Source: openai.MessageSource{Interface: "gui"}}}, false, 0},
		{"other gateway", []openai.Message{{Role: "user", Source: openai.MessageSource{Interface: "api", Gateway: "other"}}}, false, 0},
		{"unattributed", []openai.Message{matrix, {Role: "user"}}, false, 0},
		{"peer", []openai.Message{matrix}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := plugin.Preparation{Messages: tc.messages, Processing: plugin.Processing{Conversation: plugin.Conversation{Peer: tc.peer}}}
			got, err := outputHook().Before(t.Context(), p)
			if err != nil || len(got) != tc.want {
				t.Fatalf("contribution: %+v, %v", got, err)
			}
			if len(got) > 0 && (got[0].Role != "system" || got[0].Content != outputInstructions) {
				t.Fatalf("instruction: %+v", got)
			}
		})
	}
}

type outputModel struct {
	t           *testing.T
	calls, want int
}

func (m *outputModel) Complete(_ context.Context, _ string, messages []openai.Message, _ []openai.Tool) (openai.Message, error) {
	m.calls++
	count := 0
	for _, msg := range messages {
		if msg.Content == outputInstructions {
			count++
			if msg.Role != "system" {
				m.t.Fatal("instruction not system context")
			}
		}
	}
	if count != m.want {
		m.t.Fatalf("call %d: got %d copies, want %d", m.calls, count, m.want)
	}
	if m.calls%2 == 1 {
		return openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call", Type: "function", Function: openai.FunctionCall{Name: "noop", Arguments: "{}"}}}}, nil
	}
	return openai.Message{Role: "assistant", Content: "done"}, nil
}

func TestOutputInstructionsOnceAcrossRoundsAndTurns(t *testing.T) {
	k := kernel.New()
	if err := k.Start(sessionProvider(&testSessions{}), Plugin(home.Dir(t.TempDir()), nil)); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	hooks := plugin.All[plugin.AnswerHook](k.Context())
	if len(hooks) != 1 || hooks[0].Name != "matrix-output" {
		t.Fatalf("registered hooks: %+v", hooks)
	}
	model := &outputModel{t: t}
	tool := agent.NewTool("noop", "Harmless test tool", nil, func(context.Context, struct{}) (string, error) { return "ok", nil })
	bot, err := agent.New(model, "test", "base", tool).WithExtensions("", nil, hooks)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []openai.MessageSource{{Interface: "api", Gateway: "matrix"}, {Interface: "api", Gateway: "matrix"}, {Interface: "gui"}} {
		model.want = 0
		if source.Gateway == "matrix" {
			model.want = 1
		}
		if _, err := bot.Respond(openai.WithMessageSource(t.Context(), source), "same-session", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if model.calls != 6 {
		t.Fatalf("model calls: %d", model.calls)
	}
}
