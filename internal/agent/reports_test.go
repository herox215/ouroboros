package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"ouroboros/internal/openai"
)

func TestReports(t *testing.T) {
	a := &Answer{}
	first, second := make(chan struct{}), make(chan struct{})
	a.Watch(first, func() string { return "first" })
	a.Watch(second, func() string { return "second" })
	if got := a.reported(); len(got) != 0 {
		t.Fatalf("not ready: %v", got)
	}
	close(second)
	if got := a.reported(); fmt.Sprint(got) != "[second]" {
		t.Fatalf("ready: %v", got)
	}
	if got := a.reported(); len(got) != 0 {
		t.Fatalf("delivered twice: %v", got)
	}
	close(first)
	a.Watch(second, func() string { return "" })
	if got := a.reported(); fmt.Sprint(got) != "[first]" {
		t.Fatalf("remaining: %v", got)
	}
}

func TestReportDuringFinalAnswer(t *testing.T) {
	done := make(chan struct{})
	tool := Tool{Name: "work", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		AnswerOf(ctx).Watch(done, func() string { return "late result" })
		return "started", nil
	}}
	calls := 0
	llm := llmFunc(func(msgs []openai.Message) openai.Message {
		calls++
		switch calls {
		case 1:
			return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "work", Arguments: "{}"}}}}
		case 2:
			close(done)
			return openai.Message{Content: "not final yet"}
		default:
			if msgs[len(msgs)-1].Content != "late result" {
				t.Fatal("completion lost")
			}
			return openai.Message{Content: "done"}
		}
	})
	got, err := New(llm, "m", "sys", tool).Respond(context.Background(), "c", "go")
	if err != nil || got != "done" || calls != 3 {
		t.Fatalf("%q, %v, %d calls", got, err, calls)
	}
}

func TestReportFollowsToolResults(t *testing.T) {
	done := make(chan struct{})
	tool := Tool{Name: "work", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		AnswerOf(ctx).Watch(done, func() string { return "background result" })
		close(done)
		return "tool result", nil
	}}
	calls := 0
	llm := llmFunc(func(msgs []openai.Message) openai.Message {
		calls++
		if calls == 1 {
			return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "work", Arguments: "{}"}}}}
		}
		last := msgs[len(msgs)-2:]
		if last[0].Role != "tool" || last[0].Content != "tool result" || last[1].Role != "user" || last[1].Content != "background result" {
			t.Fatalf("results: %+v", last)
		}
		return openai.Message{Content: "done"}
	})
	if _, err := New(llm, "m", "sys", tool).Respond(context.Background(), "c", "go"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("%d LLM calls", calls)
	}
}

// What a tool says to the owner, and what it keeps, follows the results of
// its round as messages of their own, which the LLM reads and the next
// answer remembers; outside of an answer nothing is kept.
func TestKept(t *testing.T) {
	if Keep(context.Background(), "x") {
		t.Fatal("kept outside of an answer")
	}
	if Say(context.Background(), "x") || Say(WithSaying(context.Background(), nil), "x") {
		t.Fatal("said where nobody hears it")
	}
	var shown []string
	tool := Tool{Name: "work", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		Keep(ctx, "asked and answered")
		Say(ctx, "Alles klar.")
		return "done", nil
	}}
	var sent []openai.Message
	llm := llmFunc(func(msgs []openai.Message) openai.Message {
		sent = msgs
		if len(msgs) == 2 {
			return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "work", Arguments: "{}"}}, {ID: "2", Function: openai.FunctionCall{Name: "work", Arguments: "{}"}}}}
		}
		return openai.Message{Content: "fine"}
	})
	bot := New(llm, "m", "sys", tool)
	ctx := WithSaying(context.Background(), func(id, text string) { shown = append(shown, id+" "+text) })
	if Say(ctx, "") {
		t.Fatal("said nothing")
	}
	if got, err := bot.Respond(ctx, "c", "go"); err != nil || got != "fine" {
		t.Fatalf("%q, %v", got, err)
	}
	var roles []string
	for _, m := range sent[2:] {
		roles = append(roles, m.Role+" "+m.Content)
	}
	said := "user " + SaidHead + "Alles klar."
	if want := []string{"assistant ", "tool done", "tool done", "user asked and answered", said, "user asked and answered", said}; fmt.Sprint(roles) != fmt.Sprint(want) {
		t.Fatalf("sent %q", roles)
	}
	if len(shown) != 2 || shown[0] == shown[1] || len(shown[0]) < len(" Alles klar.") {
		t.Fatalf("shown %q", shown)
	}
	if remembered := bot.memory.history("c"); len(remembered) != 9 { // go, the calls, two results, four kept, the answer
		t.Fatalf("remembered %d messages", len(remembered))
	}
}
