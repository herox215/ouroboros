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
// message "call NAME" is answered with a call to the tool NAME and the text
// "calling NAME".
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
		return openai.Message{Content: "calling " + name, ToolCalls: []openai.ToolCall{call}}, nil
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

// An answer that failed is remembered as far as it got, closed by a note
// that does not tell why.
func TestRespondErrorIsRememberedWithoutItsCause(t *testing.T) {
	llm := &fakeLLM{err: errors.New("boom")}
	a := New(llm, "gpt", "alpha")
	if _, err := a.Respond(context.Background(), "c1", "kept"); err == nil {
		t.Fatal("want error")
	}
	llm.err = nil
	a.Respond(context.Background(), "c1", "next")
	// system, kept, the note, next
	if len(llm.msgs) != 4 || llm.msgs[1].Content != "kept" ||
		llm.msgs[2].Role != "assistant" || llm.msgs[2].Content != abandoned || llm.msgs[3].Content != "next" {
		t.Fatalf("request %+v", llm.msgs)
	}
	if strings.Contains(llm.msgs[2].Content, "boom") {
		t.Fatal("the cause must not be remembered")
	}
}

// failLLM asks for a tool on its first rounds turns and then fails.
type failLLM struct{ rounds, calls int }

func (l *failLLM) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	l.calls++
	if l.calls > l.rounds {
		return openai.Message{}, errors.New("HTTP 504")
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "noop"}}}}, nil
}

// The tool calls of an answer that failed are kept: asked again, the LLM
// goes on from their results.
func TestFailedAnswerKeepsItsToolCalls(t *testing.T) {
	a := New(&failLLM{rounds: 2}, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "result", nil }})
	if _, err := a.Respond(context.Background(), "c1", "go"); err == nil {
		t.Fatal("want error")
	}
	h := a.memory.history("c1")
	// go, call, result, call, result, the note
	if len(h) != 6 || h[0].Content != "go" || h[2].Role != "tool" || h[4].Content != "result" || h[5].Content != abandoned {
		t.Fatalf("history %+v", h)
	}
}

func TestHistoryIsTrimmed(t *testing.T) {
	llm := &fakeLLM{}
	a := New(llm, "gpt", "alpha")
	for i := range maxHistory {
		a.Respond(context.Background(), "c1", fmt.Sprint(i))
	}
	if n := len(a.memory.history("c1")); n != maxHistory {
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
	if len(a.memory.conversations) != maxContexts {
		t.Fatalf("contexts = %d", len(a.memory.conversations))
	}
	if a.memory.history("1") != nil || a.memory.history("0") == nil {
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
	if n := len(a.memory.history("c1")); n != 4 {
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

// loopLLM asks for a tool on every turn and records the last message of
// every request.
type loopLLM struct {
	calls int
	lasts []string
}

func (l *loopLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.calls++
	l.lasts = append(l.lasts, msgs[len(msgs)-1].Content)
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "noop"}}}}, nil
}

func TestEndlessToolCallsAreStopped(t *testing.T) {
	llm := &loopLLM{}
	a := New(llm, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	if _, err := a.Respond(context.Background(), "c1", "go"); !errors.Is(err, ErrTooManyToolCalls) || llm.calls != maxRounds {
		t.Fatalf("err %v after %d calls", err, llm.calls)
	}
	h := a.memory.history("c1")
	if len(h) != 2+2*maxRounds || h[0].Content != "go" || h[len(h)-1].Content != abandoned {
		t.Fatalf("an abandoned answer is remembered as far as it got: %d messages", len(h))
	}
	// The last calls are told how many there are, after the result of the tool.
	for i, last := range llm.lasts {
		told := strings.Contains(last, fmt.Sprintf("this is call %d of at most %d", i+1, maxRounds))
		if told != (i >= warnRounds) || strings.Contains(last, "Not from the tool") != told {
			t.Fatalf("call %d: %q", i+1, last)
		}
	}
	for _, m := range h {
		if strings.Contains(m.Content, "Not from the tool") {
			t.Fatalf("the count is remembered: %q", m.Content)
		}
	}
}

func TestTrimmedHistoryStartsWithUserMessage(t *testing.T) {
	a := New(&fakeLLM{}, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	for range maxHistory {
		a.Respond(context.Background(), "c1", "call noop") // four messages each
	}
	a.Respond(context.Background(), "c1", "plain") // two more: the cut now falls into a tool call
	h := a.memory.history("c1")
	if len(h) != maxHistory-2 || h[0].Role != "user" {
		t.Fatalf("history of %d messages starts with %q", len(h), h[0].Role)
	}
}

// roundsLLM asks for a tool on its first rounds turns and then answers.
type roundsLLM struct{ rounds, calls int }

func (l *roundsLLM) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	l.calls++
	if l.calls > l.rounds {
		return openai.Message{Content: "done"}, nil
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "noop"}}}}, nil
}

// An answer that took more tool calls than the history keeps messages is
// kept as a whole, from the user's message on, and what came before is not.
func TestLongAnswerIsKeptWhole(t *testing.T) {
	llm := &roundsLLM{rounds: maxHistory / 2}
	a := New(llm, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	a.memory.remember("c1", []openai.Message{{Role: "user", Content: "before"}, {Role: "assistant", Content: "earlier"}})
	if got, err := a.Respond(context.Background(), "c1", "go"); err != nil || got != "done" {
		t.Fatalf("got %q, %v", got, err)
	}
	h := a.memory.history("c1")
	if len(h) != 2+2*(maxHistory/2) || h[0].Content != "go" || h[len(h)-1].Content != "done" {
		t.Fatalf("history of %d messages starts with %q", len(h), h[0].Content)
	}
}

// A history without a message of the user is remembered as none.
func TestHistoryWithoutUserMessageIsEmpty(t *testing.T) {
	m := NewMemory()
	m.remember("c1", []openai.Message{{Role: "assistant", Content: "x"}, {Role: "tool", Content: "y"}})
	if h := m.history("c1"); len(h) != 0 {
		t.Fatalf("history %+v", h)
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
// with how long it took, how it ended and the text the LLM sent, also along
// with calls of tools; every tool call with how long it took.
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
		calls[1].Content != "calling slow" || calls[2] != (Call{}) || !calls[3].Done || calls[3].ToolCalls != 0 ||
		calls[3].Err != nil || calls[3].Content != "echo done" {
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
	a := New(&fakeLLM{}, "gpt", "sys", stop)
	if _, err := a.Run(ctx, "c1", "call stop", observe); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
	// call stop, the call, its result, the note
	if h := a.memory.history("c1"); len(h) != 4 || h[2].Role != "tool" || h[3].Content != abandoned {
		t.Fatalf("history %+v", h)
	}
}

// What a memory remembers of a conversation goes on in another one.
func TestMemoryRestore(t *testing.T) {
	m := NewMemory()
	msgs := []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	m.remember("c", msgs)
	other := NewMemory()
	other.Restore("c", m.Conversation("c"))
	other.Restore("empty", nil)
	if got := other.Conversation("c"); len(got) != 2 || got[0].Content != "hi" || got[1].Content != "hello" {
		t.Fatalf("got %+v", got)
	}
	if len(other.conversations) != 1 {
		t.Fatal("an empty conversation should not be remembered")
	}
}
