package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

func TestInputAndSteeringMessageSourcesPersist(t *testing.T) {
	input := []openai.Message{
		{Role: "user", Content: "first here", Source: openai.MessageSource{Interface: "gui"}},
		{Role: "user", Content: "second here", Source: openai.MessageSource{Interface: "cli"}},
	}
	steering := []openai.Message{
		{Role: "user", Content: "steer here", Source: openai.MessageSource{Interface: "tui"}},
		{Role: "user", Content: "also here", Source: openai.MessageSource{Interface: "api"}},
	}
	want := append(append([]openai.Message{}, input...), steering...)
	var sent [][]openai.Message
	llm := llmFunc(func(messages []openai.Message) openai.Message {
		sent = append(sent, copyMessages(messages))
		if len(sent) == 1 {
			return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Type: "function", Function: openai.FunctionCall{Name: "test", Arguments: "{}"}}}}
		}
		return openai.Message{Content: "done"}
	})
	tool := NewTool("test", "", nil, func(context.Context, struct{}) (string, error) { return "result", nil })
	var events []openai.Message
	a, err := New(llm, "m", "s", tool).WithExtensions("", nil, []AnswerHook{{After: func(_ context.Context, o AnswerOutcome) error { events = o.Messages; return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithInputMessages(t.Context(), input)
	input[0].Content = "changed after context copy"
	ctx = WithSteering(ctx, func() []string { t.Error("old callback used despite message callback"); return nil })
	ctx = WithMessageSteering(ctx, func() []openai.Message { return steering })
	if _, err := a.Run(ctx, "id", "ignored", Observer{}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || len(sent[0]) != 3 || len(sent[1]) != 7 {
		t.Fatalf("requests: %+v", sent)
	}
	var users []openai.Message
	for _, m := range events {
		if m.Role == "user" {
			users = append(users, m)
		}
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("raw events: %+v", users)
	}
	for _, m := range sent[1] {
		if m.Source != (openai.MessageSource{}) {
			t.Fatalf("metadata sent to provider: %+v", m)
		}
		if m.Role == "user" && (!strings.Contains(m.Content, "source interface = ") || !strings.Contains(m.Content, "not a screenshot")) {
			t.Fatalf("missing model context: %+v", m)
		}
	}
	stored := a.memory.Conversation("id")
	b, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var restored []openai.Message
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	memory := NewMemory()
	memory.Restore("id", restored)
	users = nil
	for _, m := range memory.Conversation("id") {
		if m.Role == "user" {
			users = append(users, m)
		}
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("restored memory: %+v", users)
	}
}

func TestMessageSourceLegacyInputAndSteering(t *testing.T) {
	a := New(&fakeLLM{}, "m", "s", NewTool("test", "", nil, func(context.Context, struct{}) (string, error) { return "result", nil }))
	ctx := openai.WithMessageSource(t.Context(), openai.MessageSource{Interface: "cli"})
	// Legacy steering still receives the context's owner source.
	ctx = WithSteering(ctx, func() []string { return []string{"later"} })
	// Initial input is deliberately unattributed so the fake calls the tool.
	ctx = WithInputMessages(ctx, []openai.Message{{Role: "user", Content: "call test"}})
	if _, err := a.Respond(ctx, "id", "ignored"); err != nil {
		t.Fatal(err)
	}
	stored := a.memory.Conversation("id")
	if stored[3].Content != "later" || stored[3].Source.Interface != "cli" {
		t.Fatalf("legacy steering: %+v", stored)
	}
	if _, err := a.Respond(openai.WithMessageSource(t.Context(), openai.MessageSource{Interface: "gui"}), "new", "here"); err != nil {
		t.Fatal(err)
	}
	if got := a.memory.Conversation("new")[0]; got.Content != "here" || got.Source.Interface != "gui" {
		t.Fatalf("legacy input: %+v", got)
	}
	ctx = WithMessageSteering(WithSteering(t.Context(), func() []string { t.Fatal("fallback called"); return nil }), func() []openai.Message { return []openai.Message{{Content: "new"}} })
	if got := Steered(ctx); len(got) != 1 || got[0] != "new" {
		t.Fatalf("Steered: %v", got)
	}
}

func TestEmptyInputMessagesOverrideText(t *testing.T) {
	llm := &fakeLLM{}
	a := New(llm, "m", "s")
	if _, err := a.Respond(WithInputMessages(t.Context(), nil), "id", "ignored"); err != nil {
		t.Fatal(err)
	}
	if len(llm.msgs) != 1 || llm.msgs[0].Role != "system" {
		t.Fatalf("input not overridden: %+v", llm.msgs)
	}
}
