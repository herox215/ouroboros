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
