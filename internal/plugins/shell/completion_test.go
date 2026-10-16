package shell

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

type completionLLM func([]openai.Message) openai.Message

func (f completionLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	return f(msgs), nil
}

func TestAutomaticCompletion(t *testing.T) {
	w := newWorld(t)
	tool := w.tool("{}")
	run := tool.Run
	tool.Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
		result, err := run(ctx, raw)
		if err == nil {
			// Complete before the tool round ends, without inspecting the result.
			p, _ := jobsOf(ctx).job(1)
			<-p.done
		}
		return result, err
	}
	calls := 0
	llm := completionLLM(func(msgs []openai.Message) openai.Message {
		calls++
		if calls == 1 {
			return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "bash", Arguments: `{"command":"echo result; exit 7","background":true}`}}}}
		}
		result := msgs[len(msgs)-1].Content
		if !strings.Contains(result, "Background bash job 1 completed") || !strings.Contains(result, "exit 7\nresult\n") {
			t.Fatalf("completion: %q", result)
		}
		return openai.Message{Content: "done"}
	})
	if _, err := agent.New(llm, "m", "sys", tool).Respond(context.Background(), "c", "go"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("%d LLM calls", calls)
	}
}

func TestJobWait(t *testing.T) {
	w := newWorld(t)
	got := w.answer("{}",
		map[string]any{"command": "sleep 0.1; echo ready", "background": true},
		map[string]any{"job": 1},
	)
	if len(got) != 2 || got[1] != "job 1 ended: exit 0\nready\n" {
		t.Fatalf("wait: %q", got)
	}
}

func TestJobWaitCanceled(t *testing.T) {
	done := make(chan struct{})
	j := &jobs{all: []*process{{done: done}}}
	// Exercise look under an actual answer, but cancel only the call, not it.
	tool := agent.Tool{Name: "wait", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		agent.AnswerOf(ctx).Value(jobsKey{}, func() any { return j })
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := look(ctx, args{Job: 1})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("wait: %v", err)
		}
		return "done", nil
	}}
	llm := &llmCalling{calls: []map[string]any{{}}}
	// llmCalling names bash; use that name for this test-only tool.
	tool.Name = "bash"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := agent.New(llm, "m", "sys", tool).Respond(ctx, "c", "go"); err != nil {
		t.Fatal(err)
	}
}
