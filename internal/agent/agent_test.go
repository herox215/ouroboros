package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// The history is cut in steps: up to maxHistory messages it stays as it is,
// beyond them it is cut to keptHistory, and its start stays until the next
// cut.
func TestHistoryIsTrimmed(t *testing.T) {
	llm := &fakeLLM{}
	a := New(llm, "gpt", "alpha")
	for i := range maxHistory / 2 {
		a.Respond(context.Background(), "c1", fmt.Sprint(i))
	}
	if h := a.memory.history("c1"); len(h) != maxHistory || h[0].Content != "0" {
		t.Fatalf("history of %d messages starts with %q", len(h), h[0].Content)
	}
	a.Respond(context.Background(), "c1", "cut")
	h := a.memory.history("c1")
	if len(h) != keptHistory || h[0].Role != "user" {
		t.Fatalf("after the cut: %d messages, starting with %q", len(h), h[0].Role)
	}
	start := h[0].Content
	a.Respond(context.Background(), "c1", "on")
	if h := a.memory.history("c1"); len(h) != keptHistory+2 || h[0].Content != start {
		t.Fatalf("after the cut, the start moved: %d messages, starting with %q", len(h), h[0].Content)
	}
}

// A history of big messages is cut by its bytes.
func TestHistoryIsTrimmedByItsSize(t *testing.T) {
	a := New(&fakeLLM{}, "gpt", "alpha")
	big := strings.Repeat("x", 30<<10)
	for i := range 4 { // 4 answers of 60 KiB: below maxHistoryBytes
		a.Respond(context.Background(), "c1", fmt.Sprint(i, big))
	}
	if h := a.memory.history("c1"); len(h) != 8 {
		t.Fatalf("before the cut: %d messages", len(h))
	}
	a.Respond(context.Background(), "c1", "5"+big)
	h := a.memory.history("c1")
	size := 0
	for _, m := range h {
		size += len(m.Content)
	}
	if size > keptHistoryBytes || len(h) != 4 || h[0].Role != "user" {
		t.Fatalf("after the cut: %d messages of %d bytes", len(h), size)
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
	args := fmt.Sprintf(`{"n":%d}`, l.calls) // never the same call twice
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "noop", Arguments: args}}}}, nil
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
	// From askRounds on, the calls are told to ask rather than search on,
	// and the last ones how many there are, after the result of the tool.
	for i, last := range llm.lasts {
		told := strings.Contains(last, fmt.Sprintf("this is call %d of at most %d", i+1, maxRounds))
		asked := strings.Contains(last, fmt.Sprintf("this is call %d of the LLM for this answer. If you have not yet found", i+1))
		if told != (i >= warnRounds) || asked != (i >= askRounds && i < warnRounds) || strings.Contains(last, "Not from the user") != (told || asked) {
			t.Fatalf("call %d: %q", i+1, last)
		}
	}
	for _, m := range h {
		if strings.Contains(m.Content, "Not from the user") {
			t.Fatalf("the count is remembered: %q", m.Content)
		}
	}
}

func TestTrimmedHistoryStartsWithUserMessage(t *testing.T) {
	a := New(&fakeLLM{}, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	for range maxHistory / 4 {
		a.Respond(context.Background(), "c1", "call noop") // four messages each
	}
	a.Respond(context.Background(), "c1", "plain") // two more: the cut to keptHistory falls into a tool call
	h := a.memory.history("c1")
	if len(h) != keptHistory-2 || h[0].Role != "user" {
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
// kept as a whole, from the user's message on. What came before it is kept
// too while cutting it would save little, and cut once the conversation is
// huge.
func TestLongAnswerIsKeptWhole(t *testing.T) {
	llm := &roundsLLM{rounds: maxHistory / 2}
	a := New(llm, "gpt", "sys", Tool{Name: "noop", Run: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	a.memory.remember("c1", []openai.Message{{Role: "user", Content: "before"}, {Role: "assistant", Content: "earlier"}})
	if got, err := a.Respond(context.Background(), "c1", "go"); err != nil || got != "done" {
		t.Fatalf("got %q, %v", got, err)
	}
	h := a.memory.history("c1")
	if len(h) != 4+2*(maxHistory/2) || h[0].Content != "before" || h[2].Content != "go" || h[len(h)-1].Content != "done" {
		t.Fatalf("history of %d messages starts with %q", len(h), h[0].Content)
	}
	huge := []openai.Message{{Role: "user", Content: "before"}, {Role: "assistant", Content: strings.Repeat("x", hugeHistoryBytes)}}
	a.memory.remember("c1", append(huge, h[2:]...))
	if h := a.memory.history("c1"); len(h) != 2+2*(maxHistory/2) || h[0].Content != "go" {
		t.Fatalf("huge: history of %d messages starts with %q", len(h), h[0].Content)
	}
}

// At a cut, what the LLM thought is left out of the messages that are kept:
// it is only needed within an answer. Without a cut a little of it stays, as
// the provider cached it.
func TestCutLeavesOutWhatWasThought(t *testing.T) {
	m := NewMemory()
	var msgs []openai.Message
	for i := range maxHistory / 2 {
		msgs = append(msgs, openai.Message{Role: "user", Content: fmt.Sprint(i)},
			openai.Message{Role: "assistant", Content: "ok", Reasoning: "hm", ReasoningField: "reasoning_content"})
	}
	m.remember("c1", msgs)
	if h := m.history("c1"); len(h) != maxHistory || h[1].Reasoning != "hm" {
		t.Fatalf("without a cut: %d messages, %+v", len(h), h[1])
	}
	m.remember("c1", append(msgs, openai.Message{Role: "user", Content: "more"}, openai.Message{Role: "assistant", Content: "ok", Reasoning: "hm"}))
	h := m.history("c1")
	if len(h) != keptHistory {
		t.Fatalf("after the cut: %d messages", len(h))
	}
	for _, msg := range h {
		if msg.Reasoning != "" || msg.ReasoningField != "" {
			t.Fatalf("after the cut: %+v", msg)
		}
	}
	if msgs[1].Reasoning != "hm" {
		t.Fatal("the messages given were changed")
	}
	// Once what was thought is half of what comes after its start, it is
	// left out without a cut.
	long := strings.Repeat("hm ", 100)
	m.remember("c2", []openai.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "ok"},
		{Role: "user", Content: "b"}, {Role: "assistant", Content: "ok", Reasoning: long, ReasoningField: "reasoning_content"}})
	if h := m.history("c2"); len(h) != 4 || h[3].Reasoning != "" || h[3].ReasoningField != "" {
		t.Fatalf("much thought: %+v", h)
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
	slow := Tool{Name: "slow", Schema: `{"type":"object"}`, Run: func(context.Context, json.RawMessage) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return "done", nil
	}}
	var calls []Call
	var steps []Step
	observe := Observer{Call: func(c Call) { calls = append(calls, c) }, Step: func(s Step) { steps = append(steps, s) }}
	if _, err := New(&fakeLLM{}, "gpt", "sys", slow).Run(context.Background(), "c1", "call slow", observe); err != nil {
		t.Fatal(err)
	}
	// What a call sent is the messages and the tools as JSON.
	msgs, _ := json.Marshal([]openai.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "call slow"}})
	specs, _ := json.Marshal([]openai.Tool{openai.NewTool("slow", "", `{"type":"object"}`)})
	first := len(msgs) + len(specs)
	if len(specs) < 50 {
		t.Fatalf("the tools are part of what is sent: %s", specs)
	}
	if len(calls) != 4 || calls[0] != (Call{Sent: first}) || !calls[1].Done || calls[1].ToolCalls != 1 || calls[1].Err != nil ||
		calls[1].Content != "calling slow" || calls[1].Sent != first || calls[2].Sent <= first || calls[2] != (Call{Sent: calls[2].Sent}) ||
		!calls[3].Done || calls[3].ToolCalls != 0 || calls[3].Err != nil || calls[3].Content != "echo done" || calls[3].Sent != calls[2].Sent {
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
	other.Forget("c")
	other.Forget("none")
	if got := other.Conversation("c"); got != nil {
		t.Fatalf("forgotten: %+v", got)
	}
}

// toolsLLM asks for the tool on its first rounds calls, reports what it was
// told of the API's usage, and remembers what it was sent.
type toolsLLM struct {
	rounds, calls int
	sent          [][]openai.Message
}

func (l *toolsLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.calls++
	l.sent = append(l.sent, msgs)
	usage := openai.Usage{Prompt: 100 * l.calls, Cached: 50 * l.calls}
	if l.calls > l.rounds {
		return openai.Message{Content: "done", Usage: usage}, nil
	}
	call := openai.FunctionCall{Name: "dump", Arguments: fmt.Sprintf(`{"n":%d}`, l.calls)}
	return openai.Message{Usage: usage, ToolCalls: []openai.ToolCall{{ID: fmt.Sprint("c", l.calls), Function: call}}}, nil
}

// What a result repeats of an earlier result of the same answer is left out
// of it; what is new stays, and so do short repeats.
func TestRepeatedResultsAreLeftOut(t *testing.T) {
	var file []string
	for i := range 100 {
		file = append(file, fmt.Sprintf("line %03d of the file, long enough to count", i))
	}
	results := []string{
		strings.Join(file[:60], "\n"),
		"new\n" + strings.Join(file[10:70], "\n") + "\nshort\n" + file[1] + "\n",
	}
	llm := &toolsLLM{rounds: len(results)}
	n := 0
	a := New(llm, "gpt", "sys", Tool{Name: "dump", Run: func(context.Context, json.RawMessage) (string, error) {
		n++
		return results[n-1], nil
	}})
	if _, err := a.Respond(context.Background(), "c1", "go"); err != nil {
		t.Fatal(err)
	}
	h := a.memory.history("c1")
	want := "new\n[50 lines, " + fmt.Sprint(len(strings.Join(file[10:60], "\n"))+1) +
		" bytes, left out: an earlier result of this answer has them]\n" +
		strings.Join(file[60:70], "\n") + "\nshort\n" + file[1] + "\n"
	if h[2].Content != results[0] || h[4].Content != want {
		t.Fatalf("results:\n%s\n---\n%s", h[2].Content, h[4].Content)
	}
	// An earlier answer does not count: it may be cut from the conversation.
	n = 0
	llm.calls = 0
	a.Respond(context.Background(), "c1", "again")
	if h := a.memory.history("c1"); h[len(h)-4].Content != results[0] {
		t.Fatalf("the first result of the next answer: %q", h[len(h)-4].Content)
	}
}

// Beyond Budget, the LLM is told with every call how much the results of the
// answer have, and Spent says so to the tools; what the API said of a call
// is reported.
func TestBudgetOfAnAnswer(t *testing.T) {
	llm := &toolsLLM{rounds: 3}
	var spent []int
	a := New(llm, "gpt", "sys", Tool{Name: "dump", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		spent = append(spent, Spent(ctx))
		return fmt.Sprint(len(spent)) + strings.Repeat("y", Budget/2), nil
	}})
	var calls []Call
	if _, err := a.Run(context.Background(), "c1", "go", Observer{Call: func(c Call) {
		if c.Done {
			calls = append(calls, c)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spent, []int{0, Budget/2 + 1, Budget + 2}) {
		t.Fatalf("spent %v", spent)
	}
	// The note is a message of its own after the results, which stay as
	// they are remembered: the next call begins like this one.
	for i, msgs := range llm.sent {
		last := msgs[len(msgs)-1]
		told := last.Role == "user" && strings.HasPrefix(last.Content, "[Not from the user: the results of tools of this answer have")
		if told != (i >= 2) {
			t.Errorf("call %d: told %v", i+1, told)
		}
		if told && (msgs[len(msgs)-2].Role != "tool" || strings.Contains(msgs[len(msgs)-2].Content, "Not from")) {
			t.Errorf("call %d: the result before the note %+v", i+1, msgs[len(msgs)-2])
		}
	}
	if next := llm.sent[3]; !slices.EqualFunc(llm.sent[2][:len(llm.sent[2])-1], next[:len(llm.sent[2])-1], func(a, b openai.Message) bool {
		return a.Role == b.Role && a.Content == b.Content
	}) {
		t.Error("the next call does not begin like the last one")
	}
	if h := a.memory.history("c1"); strings.Contains(h[len(h)-2].Content, "Not from the user") {
		t.Error("what the LLM was told is remembered")
	}
	if len(calls) != 4 || calls[3].Tokens != 400 || calls[3].Cached != 200 {
		t.Errorf("calls %+v", calls)
	}
}

// The same call of a tool asked for a third time in a row is not run: the
// LLM is told so instead. Two in a row, or the same with another between,
// are run.
func TestRepeatedCallIsNotRun(t *testing.T) {
	args := []string{`{"a":1}`, `{"a":1}`, `{"b":2}`, `{"b":2}`, `{"a":1}`, `{"a":1}`, `{"a":1}`, `{"a":1}`}
	i := 0
	llm := llmFunc(func(msgs []openai.Message) openai.Message {
		if i == len(args) {
			return openai.Message{Content: "done"}
		}
		i++
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: fmt.Sprint("c", i), Function: openai.FunctionCall{Name: "noop", Arguments: args[i-1]}}}}
	})
	var ran []string
	a := New(llm, "gpt", "sys", Tool{Name: "noop", Run: func(_ context.Context, raw json.RawMessage) (string, error) {
		ran = append(ran, string(raw))
		return "ok", nil
	}})
	if _, err := a.Respond(context.Background(), "c1", "go"); err != nil {
		t.Fatal(err)
	}
	if want := args[:6]; !slices.Equal(ran, want) {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	h := a.memory.history("c1")
	for _, n := range []int{14, 16} {
		if !strings.HasPrefix(h[n].Content, "error: this is the call of noop you asked for 3 times in a row") {
			t.Fatalf("message %d: %q", n, h[n].Content)
		}
	}
}

// llmFunc is an LLM that answers with a function of what it is sent.
type llmFunc func([]openai.Message) openai.Message

func (f llmFunc) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	return f(msgs), nil
}

// padded returns msgs with the content of msgs[i] made as long as it takes
// for all of them to have size bytes as JSON.
func padded(t *testing.T, msgs []openai.Message, i, size int) []openai.Message {
	t.Helper()
	msgs = slices.Clone(msgs)
	msgs[i].Content += strings.Repeat("x", size-sizes(msgs)[0])
	if got := sizes(msgs)[0]; got != size {
		t.Fatalf("padded to %d bytes, want %d", got, size)
	}
	return msgs
}

// The bounds of a cut: at maxHistoryBytes nothing is cut, one byte more is
// cut when that keeps at most half of the bytes or of the messages.
func TestCutAtItsBounds(t *testing.T) {
	u := func(s string) openai.Message { return openai.Message{Role: "user", Content: s} }
	as := func(s string) openai.Message { return openai.Message{Role: "assistant", Content: s} }
	tool := func(s string) openai.Message { return openai.Message{Role: "tool", Content: s} }
	starts := func(msgs []openai.Message) string { return trim(msgs)[0].Content[:2] }

	small := []openai.Message{u("u0"), as("a0"), u("u1"), as("a1")}
	if got := starts(padded(t, small, 1, maxHistoryBytes)); got != "u0" {
		t.Errorf("at maxHistoryBytes: starts with %q", got)
	}
	if got := starts(padded(t, small, 1, maxHistoryBytes+1)); got != "u1" {
		t.Errorf("beyond maxHistoryBytes: starts with %q", got)
	}
	// Most bytes, half the messages: cut.
	if got := starts(padded(t, small, 3, maxHistoryBytes+1)); got != "u1" {
		t.Errorf("half the messages kept: starts with %q", got)
	}
	// More than half the messages, few bytes: cut.
	more := []openai.Message{u("u0"), as("a0"), u("u1"), as("a1"), u("u2"), as("a2")}
	if got := starts(padded(t, more, 1, maxHistoryBytes+1)); got != "u1" {
		t.Errorf("few bytes kept: starts with %q", got)
	}
	// More than half the messages and half the bytes exactly: cut; one byte
	// more of what is kept, and it is not.
	long := []openai.Message{u("u0"), as("a0"), u("u1"), {Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "c"}}}, tool("t1"), as("a1")}
	half := padded(t, long[2:], 3, 200<<10)
	exact := padded(t, append(long[:2:2], half...), 1, 400<<10)
	if got := starts(exact); got != "u1" {
		t.Errorf("half the bytes kept: starts with %q", got)
	}
	half[3].Content += "x"
	if got := starts(append(exact[:2:2], half...)); got != "u0" {
		t.Errorf("more than half the bytes kept: starts with %q", got)
	}
	// Up to hugeHistoryBytes such a cut is not made, beyond it it is.
	if got := starts(padded(t, long, 5, hugeHistoryBytes)); got != "u0" {
		t.Errorf("at hugeHistoryBytes: starts with %q", got)
	}
	if got := starts(padded(t, long, 5, hugeHistoryBytes+1)); got != "u1" {
		t.Errorf("beyond hugeHistoryBytes: starts with %q", got)
	}
}

// What was thought is left out without a cut when it is at least half of the
// conversation from the first message that has it on, and not before.
func TestThoughtAtItsBound(t *testing.T) {
	msgs := []openai.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: strings.Repeat("y", 2000)},
		{Role: "user", Content: "b"}, {Role: "assistant", Content: "ok", Reasoning: "r", ReasoningField: "reasoning_content"}}
	// The size of the last message is c plus what it thought: half of it
	// when it thought c bytes.
	c := sizes(msgs[3:])[0] - 1
	msgs[3].Reasoning = strings.Repeat("r", c)
	if got := trim(msgs); got[3].Reasoning != "" {
		t.Errorf("half of it was thought: kept")
	}
	msgs[3].Reasoning = strings.Repeat("r", c-1)
	if got := trim(msgs); got[3].Reasoning == "" {
		t.Errorf("less than half of it was thought: left out")
	}
}

// A run of repeated lines of minRepeated bytes is left out, one byte less is
// not.
func TestRepeatedAtItsBound(t *testing.T) {
	line := strings.Repeat("z", minRepeated-1)
	earlier := []openai.Message{{Role: "tool", Content: line + "\n" + line[1:]}}
	if got, want := unrepeated(line+"\n", earlier), fmt.Sprintf("[1 lines, %d bytes, left out: an earlier result of this answer has them]\n", minRepeated); got != want {
		t.Errorf("minRepeated bytes: %q", got)
	}
	if got := unrepeated(line[1:]+"\n", earlier); got != line[1:]+"\n" {
		t.Errorf("one byte less: %q", got)
	}
}

// The LLM is told how much the results have only beyond Budget, not at it.
func TestBudgetIsABound(t *testing.T) {
	results := []string{strings.Repeat("a", Budget), "b"}
	var told []bool
	i := 0
	llm := llmFunc(func(msgs []openai.Message) openai.Message {
		told = append(told, strings.Contains(msgs[len(msgs)-1].Content, "Not from the user"))
		if i == len(results) {
			return openai.Message{Content: "done"}
		}
		i++
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "dump", Arguments: fmt.Sprint(i)}}}}
	})
	n := 0
	a := New(llm, "gpt", "sys", Tool{Name: "dump", Run: func(context.Context, json.RawMessage) (string, error) {
		n++
		return results[n-1], nil
	}})
	if _, err := a.Respond(context.Background(), "c1", "go"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(told, []bool{false, false, true}) {
		t.Fatalf("told %v", told)
	}
}
