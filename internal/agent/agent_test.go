package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	got, err := a.Run(context.Background(), "c1", "call shout", Observer{Step: func(s Step) { steps = append(steps, s) }})
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
		got, err := New(&fakeLLM{}, "gpt", "sys", fail).Run(context.Background(), "c1", "call "+name, Observer{Step: func(s Step) { last = s }})
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

// A tool that runs other tools of its agent reports their calls like the LLM's
// own, to whoever observes the conversation.
func TestInvoke(t *testing.T) {
	shout := Tool{Name: "shout", Run: func(ctx context.Context, args json.RawMessage) (string, error) {
		return "LOUD " + ContextID(ctx) + " " + string(args), nil
	}}
	fail := Tool{Name: "fail", Run: func(context.Context, json.RawMessage) (string, error) { return "", errTest }}
	both := Tool{Name: "both", Schema: `{"type":"object"}`, Run: func(ctx context.Context, args json.RawMessage) (string, error) {
		first, err := Invoke(ctx, shout, json.RawMessage(`{"n":1}`))
		if err != nil {
			return "", err
		}
		_, err = Invoke(ctx, fail, args)
		return first, err
	}}

	var steps []string
	observe := func(s Step) {
		steps = append(steps, fmt.Sprint(s.Tool, " ", string(s.Args), " ", s.Done, " ", s.Result, " ", s.Err))
	}
	got, err := New(&fakeLLM{}, "gpt", "sys", both).Run(context.Background(), "c1", "call both", Observer{Step: observe})
	if err != nil || got != "echo error: test error" {
		t.Fatalf("got %q, %v", got, err)
	}
	want := []string{
		`both {"x":1} false  <nil>`,
		`shout {"n":1} false  <nil>`,
		`shout {"n":1} true LOUD c1 {"n":1} <nil>`,
		`fail {"x":1} false  <nil>`,
		`fail {"x":1} true  test error`,
		`both {"x":1} true LOUD c1 {"n":1} test error`,
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(steps, "\n"), strings.Join(want, "\n"))
	}

	// Nobody has to observe: not the conversation, and not a call outside of one.
	if got, err := New(&fakeLLM{}, "gpt", "sys", both).Respond(context.Background(), "c1", "call both"); err != nil || got != "echo error: test error" {
		t.Fatalf("not observed: %q, %v", got, err)
	}
	if got, err := Invoke(context.Background(), shout, json.RawMessage(`{}`)); err != nil || got != "LOUD  {}" {
		t.Fatalf("outside of a conversation: %q, %v", got, err)
	}
}

// Every call of the LLM is reported when it starts and when it has ended,
// with how long it took and how it ended; every tool call with how long it
// took.
func TestRunReportsTheCallsOfTheLLM(t *testing.T) {
	slow := Tool{Name: "slow", Run: func(context.Context, json.RawMessage) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return "done", nil
	}}
	var calls []Call
	var steps []Step
	observe := Observer{Call: func(c Call) { calls = append(calls, c) }, Step: func(s Step) { steps = append(steps, s) }}
	if _, err := New(&fakeLLM{}, "gpt", "sys", slow).Run(context.Background(), "c1", "call slow", observe); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[0] != (Call{}) || !calls[1].Done || calls[1].ToolCalls != 1 || calls[1].Err != nil ||
		calls[2] != (Call{}) || !calls[3].Done || calls[3].ToolCalls != 0 || calls[3].Err != nil {
		t.Fatalf("calls %+v", calls)
	}
	if len(steps) != 2 || steps[0].Took != 0 || steps[1].Took < 5*time.Millisecond {
		t.Fatalf("steps %+v", steps)
	}

	calls = nil
	if _, err := New(&fakeLLM{err: errTest}, "gpt", "sys").Run(context.Background(), "c1", "hi", observe); err != errTest {
		t.Fatalf("err %v", err)
	}
	if len(calls) != 2 || !calls[1].Done || calls[1].Err != errTest {
		t.Fatalf("a call that failed: %+v", calls)
	}
}

// An answer that was stopped while a tool ran does not ask the LLM again.
func TestRunStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := Tool{Name: "stop", Run: func(context.Context, json.RawMessage) (string, error) {
		cancel()
		return "", nil
	}}
	calls := 0
	observe := Observer{Call: func(c Call) {
		if !c.Done {
			calls++
		}
	}}
	if _, err := New(&fakeLLM{}, "gpt", "sys", stop).Run(ctx, "c1", "call stop", observe); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}
