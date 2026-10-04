package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

// fakeLLM records the last request and echoes the last message. A user
// message "call NAME" is answered with a call to the tool NAME.
type fakeLLM struct {
	model string
	msgs  []openai.Message
	tools []openai.Tool
	err   error
}

func (f *fakeLLM) Complete(_ context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	f.model, f.msgs, f.tools = model, msgs, tools
	if f.err != nil {
		return openai.Message{}, f.err
	}
	last := msgs[len(msgs)-1]
	if name, ok := strings.CutPrefix(last.Content, "call "); ok && last.Role == "user" {
		call := openai.ToolCall{ID: "c1", Type: "function", Function: openai.FunctionCall{Name: name, Arguments: `{"x":1}`}}
		return openai.Message{ToolCalls: []openai.ToolCall{call}}, nil
	}
	return openai.Message{Role: "assistant", Content: "echo " + last.Content}, nil
}

func TestRespondSendsSystemPromptAndModel(t *testing.T) {
	llm := &fakeLLM{}
	a := New(llm, "gpt", "alpha")
	got, err := a.Respond(context.Background(), "c1", "hi")
	if err != nil || got != "echo hi" {
		t.Fatalf("got %q, %v", got, err)
	}
	if llm.model != "gpt" || len(llm.msgs) != 2 || llm.msgs[0].Role != "system" {
		t.Fatalf("unexpected request %s %+v", llm.model, llm.msgs)
	}
}

func TestRespondKeepsHistoryPerContext(t *testing.T) {
	llm := &fakeLLM{}
	a := New(llm, "gpt", "alpha")
	ctx := context.Background()
	a.Respond(ctx, "c1", "one")
	a.Respond(ctx, "c1", "two")
	if len(llm.msgs) != 4 { // system, one, echo one, two
		t.Fatalf("want 4 messages, got %+v", llm.msgs)
	}
	a.Respond(ctx, "c2", "other")
	if len(llm.msgs) != 2 {
		t.Fatalf("new context should start fresh, got %+v", llm.msgs)
	}
}

func TestRespondErrorIsNotRemembered(t *testing.T) {
	llm := &fakeLLM{err: errors.New("boom")}
	a := New(llm, "gpt", "alpha")
	if _, err := a.Respond(context.Background(), "c1", "lost"); err == nil {
		t.Fatal("want error")
	}
	llm.err = nil
	a.Respond(context.Background(), "c1", "next")
	if len(llm.msgs) != 2 {
		t.Fatalf("failed message must not be kept, got %+v", llm.msgs)
	}
}

func TestHistoryIsTrimmed(t *testing.T) {
	llm := &fakeLLM{}
	a := New(llm, "gpt", "alpha")
	for i := range maxHistory {
		a.Respond(context.Background(), "c1", fmt.Sprint(i))
	}
	if n := len(a.history("c1")); n != maxHistory {
		t.Fatalf("history len = %d, want %d", n, maxHistory)
	}
}

func TestLeastRecentlyUsedContextIsEvicted(t *testing.T) {
	a := New(&fakeLLM{}, "gpt", "alpha")
	ctx := context.Background()
	for i := range maxContexts {
		a.Respond(ctx, fmt.Sprint(i), "x")
	}
	a.Respond(ctx, "0", "touch") // 0 is now recent, 1 is oldest
	a.Respond(ctx, "new", "x")
	if len(a.conversations) != maxContexts {
		t.Fatalf("contexts = %d", len(a.conversations))
	}
	if a.history("1") != nil || a.history("0") == nil {
		t.Fatal("expected context 1 to be evicted and 0 to survive")
	}
}

func TestToolCall(t *testing.T) {
	llm := &fakeLLM{}
	shout := Tool{Name: "shout", Description: "d", Schema: `{"type":"object"}`,
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			return "LOUD " + ContextID(ctx) + " " + string(args), nil
		}}
	a := New(llm, "gpt", "sys", shout)

	var steps []Step
	got, err := a.Run(context.Background(), "c1", "call shout", func(s Step) { steps = append(steps, s) })
	if err != nil || got != `echo LOUD c1 {"x":1}` {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(llm.tools) != 1 || llm.tools[0].Function.Name != "shout" {
		t.Fatalf("tools offered: %+v", llm.tools)
	}
	if len(steps) != 2 || steps[0].Done || !steps[1].Done || steps[1].Result != `LOUD c1 {"x":1}` {
		t.Fatalf("steps %+v", steps)
	}
	// system, user, assistant call, tool result
	if len(llm.msgs) != 4 || llm.msgs[3].Role != "tool" || llm.msgs[3].ToolCallID != "c1" {
		t.Fatalf("request %+v", llm.msgs)
	}
	if n := len(a.history("c1")); n != 4 {
		t.Fatalf("history should keep the tool call, got %d messages", n)
	}
}

func TestToolErrorsAreToldToTheLLM(t *testing.T) {
	fail := Tool{Name: "fail", Run: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("boom")
	}}
	for name, want := range map[string]string{"fail": "echo error: boom", "nope": "echo error: unknown tool nope"} {
		var last Step
		got, err := New(&fakeLLM{}, "gpt", "sys", fail).Run(context.Background(), "c1", "call "+name, func(s Step) { last = s })
		if err != nil || got != want || last.Err == nil {
			t.Errorf("%s: got %q, %v, step %+v", name, got, err, last)
		}
	}
}

// loopLLM asks for a tool on every turn.
type loopLLM struct{ calls int }

func (l *loopLLM) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	l.calls++
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "noop"}}}}, nil
}

func TestEndlessToolCallsAreStopped(t *testing.T) {
	llm := &loopLLM{}
	a := New(llm, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	if _, err := a.Respond(context.Background(), "c1", "go"); !errors.Is(err, ErrTooManyToolCalls) || llm.calls != maxRounds {
		t.Fatalf("err %v after %d calls", err, llm.calls)
	}
	if a.history("c1") != nil {
		t.Fatal("an abandoned answer must not be remembered")
	}
}

func TestTrimmedHistoryStartsWithUserMessage(t *testing.T) {
	a := New(&fakeLLM{}, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	for range maxHistory {
		a.Respond(context.Background(), "c1", "call noop") // four messages each
	}
	a.Respond(context.Background(), "c1", "plain") // two more: the cut now falls into a tool call
	h := a.history("c1")
	if len(h) != maxHistory-2 || h[0].Role != "user" {
		t.Fatalf("history of %d messages starts with %q", len(h), h[0].Role)
	}
}
