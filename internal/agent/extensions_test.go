package agent

import (
	"context"
	"errors"
	"ouroboros/internal/a2a"
	"ouroboros/internal/openai"
	"reflect"
	"strings"
	"testing"
)

func TestProviderSelection(t *testing.T) {
	p := ContextProvider{Name: "p", Prepare: func(context.Context, Preparation) ([]openai.Message, error) {
		t.Fatal("inactive provider called")
		return nil, nil
	}}
	a, err := New(&fakeLLM{}, "m", "s").WithExtensions("", []ContextProvider{p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Respond(context.Background(), "id", "hi"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		ps       []ContextProvider
		contains string
	}{{"missing", nil, "not registered"}, {"p", []ContextProvider{p, p}, "conflicting"}, {"p", []ContextProvider{{Name: "p"}}, "missing Prepare"}} {
		_, err := a.WithExtensions(test.name, test.ps, nil)
		if err == nil || !strings.Contains(err.Error(), test.contains) {
			t.Fatalf("%v", err)
		}
	}
}
func TestProviderToolLoopAndHooks(t *testing.T) {
	llm := &fakeLLM{}
	var order []string
	var events []openai.Message
	prepares := 0
	tool := NewTool("test", "test", nil, func(context.Context, struct{}) (string, error) { return "result", nil })
	p := ContextProvider{Name: "p", Observe: func(ctx context.Context, p Processing, m openai.Message) error {
		if !p.Conversation.Peer || p.Conversation.Origin.PublicKey != "peer" {
			t.Fatal(p.Conversation)
		}
		events = append(events, m)
		return nil
	}, Prepare: func(ctx context.Context, p Preparation) ([]openai.Message, error) {
		prepares++
		if len(p.Context) != 2 {
			t.Fatalf("context: %+v", p.Context)
		}
		// Direct model use must not recurse through preparation or hooks.
		if _, err := p.Model.Complete(ctx, p.ModelName, []openai.Message{{Role: "user", Content: "processing"}}, nil); err != nil {
			t.Fatal(err)
		}
		return append(append([]openai.Message{p.Messages[0]}, p.Context...), p.Messages[1:]...), nil
	}}
	hooks := []AnswerHook{
		{Name: "one", Before: func(_ context.Context, p Preparation) ([]openai.Message, error) {
			order = append(order, "before one")
			return []openai.Message{{Role: "system", Content: "one"}}, nil
		}, After: func(_ context.Context, o AnswerOutcome) error {
			order = append(order, "after one")
			if o.Err != nil || o.Reply != "echo result" || len(o.Messages) != 4 {
				t.Fatalf("outcome %+v", o)
			}
			return errors.New("postprocess")
		}},
		{Name: "two", Before: func(_ context.Context, p Preparation) ([]openai.Message, error) {
			order = append(order, "before two")
			if len(p.Context) != 1 {
				t.Fatal(p.Context)
			}
			return []openai.Message{{Role: "system", Content: "two"}}, nil
		}, After: func(context.Context, AnswerOutcome) error { order = append(order, "after two"); return nil }},
	}
	a, err := New(llm, "m", "s", tool).WithExtensions("p", []ContextProvider{p}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := 0
	reply, err := a.Run(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), "id", "call test", Observer{Diagnostic: func(d Diagnostic) {
		diagnostics++
		if d.Plugin != "one" || d.Phase != "after" {
			t.Fatal(d)
		}
	}})
	if err != nil || reply != "echo result" || prepares != 2 || len(events) != 4 || diagnostics != 1 {
		t.Fatalf("%s %v %d %+v", reply, err, prepares, events)
	}
	if !reflect.DeepEqual(order, []string{"before one", "before two", "after one", "after two"}) {
		t.Fatal(order)
	}
	snapshot, ok := a.Inspection("id")
	if !ok || len(snapshot.Messages) != 6 || len(snapshot.Diagnostics) != 1 {
		t.Fatal(snapshot)
	}
	snapshot.Messages[0].Content = "mutated"
	again, _ := a.Inspection("id")
	if again.Messages[0].Content != "s" {
		t.Fatal("snapshot alias")
	}
	if _, ok := a.Inspection("other"); ok {
		t.Fatal("inspection leaked")
	}
	if len(a.memory.Conversation("id")) != 4 {
		t.Fatal("hook context persisted")
	}
}
func TestProviderFailureAndCancellation(t *testing.T) {
	for _, phase := range []string{"prepare", "observe", "before", "model", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			boom := errors.New("boom")
			llm := &fakeLLM{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			after := 0
			p := ContextProvider{Name: "p", Prepare: func(_ context.Context, p Preparation) ([]openai.Message, error) {
				if phase == "prepare" {
					return nil, boom
				}
				return p.Messages, nil
			}, Observe: func(context.Context, Processing, openai.Message) error {
				if phase == "observe" {
					return boom
				}
				return nil
			}}
			h := AnswerHook{Name: "h", Before: func(context.Context, Preparation) ([]openai.Message, error) {
				if phase == "before" {
					return nil, boom
				}
				if phase == "cancel" {
					cancel()
				}
				return nil, nil
			}, After: func(_ context.Context, o AnswerOutcome) error {
				after++
				if o.Err == nil {
					t.Fatal("missing outcome error")
				}
				return errors.New("after failed")
			}}
			if phase == "model" {
				llm.err = boom
			}
			a, _ := New(llm, "m", "s").WithExtensions("p", []ContextProvider{p}, []AnswerHook{h})
			reply, err := a.Respond(ctx, "id", "hi")
			if reply != "" || err == nil || after != 1 {
				t.Fatalf("%q %v %d", reply, err, after)
			}
			if phase == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, boom) {
				t.Fatal(err)
			}
			if (phase == "prepare" || phase == "observe" || phase == "before" || phase == "cancel") && llm.msgs != nil {
				t.Fatal("model called after extension failure")
			}
			snap, _ := a.Inspection("id")
			if len(snap.Diagnostics) != 1 {
				t.Fatal(snap)
			}
		})
	}
}
func TestEventsBeforeTrimming(t *testing.T) {
	count := 0
	p := ContextProvider{Name: "p", Observe: func(context.Context, Processing, openai.Message) error { count++; return nil }, Prepare: func(_ context.Context, p Preparation) ([]openai.Message, error) {
		return []openai.Message{p.Messages[0], p.Messages[len(p.Messages)-1]}, nil
	}}
	a, _ := New(&fakeLLM{}, "m", "s").WithExtensions("p", []ContextProvider{p}, nil)
	for range 100 {
		if _, err := a.Respond(context.Background(), "id", strings.Repeat("x", 4096)); err != nil {
			t.Fatal(err)
		}
	}
	if count != 200 || len(a.memory.Conversation("id")) >= 200 {
		t.Fatalf("events %d history %d", count, len(a.memory.Conversation("id")))
	}
}
func TestPreparedValidation(t *testing.T) {
	system := openai.Message{Role: "system", Content: "s"}
	call := openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "a"}}}
	result := openai.Message{Role: "tool", ToolCallID: "a"}
	for _, ms := range [][]openai.Message{nil, {{Role: "system", Content: "changed"}}, {system, result}, {system, call}, {system, call, {Role: "user"}}, {system, {Role: "user", ToolCalls: call.ToolCalls}}, {system, {Role: "assistant", ToolCalls: []openai.ToolCall{{ID: ""}}}}, {system, {Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "a"}, {ID: "a"}}}}} {
		if validatePrepared(system, ms) == nil {
			t.Fatalf("accepted %+v", ms)
		}
	}
	for _, m := range []openai.Message{{Role: "invalid"}, {Role: "user", ToolCallID: "a"}} {
		if validatePrepared(system, []openai.Message{system, m}) == nil {
			t.Fatal("invalid role accepted")
		}
	}
	if err := validatePrepared(system, []openai.Message{system, call, result}); err != nil {
		t.Fatal(err)
	}
	a, _ := New(&fakeLLM{}, "m", "s").WithExtensions("p", []ContextProvider{{Name: "p", Prepare: func(context.Context, Preparation) ([]openai.Message, error) { return nil, nil }}}, nil)
	if _, err := a.Respond(context.Background(), "id", "hi"); err == nil {
		t.Fatal("invalid request accepted")
	}
}
func TestConversationSerialization(t *testing.T) {
	m := NewMemory()
	release, err := m.acquire(context.Background(), "id")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.acquire(ctx, "id"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	other, err := m.acquire(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	if len(m.active) != 0 {
		t.Fatal("lock state retained")
	}
}

func TestExtensionFailureBoundaries(t *testing.T) {
	for _, role := range []string{"assistant", "system"} {
		t.Run(role, func(t *testing.T) {
			h := AnswerHook{Name: "invalid", Before: func(context.Context, Preparation) ([]openai.Message, error) {
				return []openai.Message{{Role: role, ToolCallID: "invalid"}}, nil
			}}
			a, _ := New(&fakeLLM{}, "m", "s").WithExtensions("", nil, []AnswerHook{h})
			if _, err := a.Respond(context.Background(), "id", "hi"); err == nil {
				t.Fatal("invalid hook context accepted")
			}
		})
	}
	llm := &fakeLLM{}
	a, _ := New(llm, "m", "s").WithExtensions("", nil, []AnswerHook{{Before: func(context.Context, Preparation) ([]openai.Message, error) {
		return []openai.Message{{Role: "system", Content: "extra"}}, nil
	}}})
	if _, err := a.Respond(context.Background(), "id", "hi"); err != nil {
		t.Fatal(err)
	}
	if len(llm.msgs) != 3 || llm.msgs[1].Content != "extra" {
		t.Fatal("hook context missing from actual request", llm.msgs)
	}
	for _, failAt := range []int{2, 3} {
		seen := 0
		boom := errors.New("observe stopped")
		p := ContextProvider{Name: "p", Prepare: func(_ context.Context, p Preparation) ([]openai.Message, error) { return p.Messages, nil }, Observe: func(context.Context, Processing, openai.Message) error {
			seen++
			if seen == failAt {
				return boom
			}
			return nil
		}}
		a, _ := New(&fakeLLM{}, "m", "s", NewTool("test", "", nil, func(context.Context, struct{}) (string, error) { return "result", nil })).WithExtensions("p", []ContextProvider{p}, nil)
		if _, err := a.Respond(context.Background(), "id", "call test"); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, _ = New(&fakeLLM{}, "m", "s").WithExtensions("p", []ContextProvider{{Name: "p", Prepare: func(_ context.Context, p Preparation) ([]openai.Message, error) { cancel(); return p.Messages, nil }}}, nil)
	if _, err := a.Respond(ctx, "id", "hi"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release, _ := a.memory.acquire(context.Background(), "id")
	if _, err := a.Respond(ctx, "id", "waiting"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
}

func TestProviderReplacement(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		providers := []ContextProvider{}
		for _, n := range []string{"first", "second"} {
			providers = append(providers, ContextProvider{Name: n, Prepare: func(_ context.Context, p Preparation) ([]openai.Message, error) {
				return []openai.Message{p.Messages[0], {Role: "user", Content: n}}, nil
			}})
		}
		a, err := New(&fakeLLM{}, "m", "s").WithExtensions(name, providers, nil)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := a.Respond(context.Background(), "id", "hi")
		if err != nil || reply != "echo "+name {
			t.Fatal(reply, err)
		}
	}
}

type blockingModel struct {
	started chan string
	finish  chan struct{}
}

func (m blockingModel) Complete(ctx context.Context, _ string, _ []openai.Message, _ []openai.Tool) (openai.Message, error) {
	select {
	case m.started <- ContextID(ctx):
	case <-ctx.Done():
		return openai.Message{}, ctx.Err()
	}
	select {
	case <-m.finish:
		return openai.Message{Content: "done"}, nil
	case <-ctx.Done():
		return openai.Message{}, ctx.Err()
	}
}
func TestSameConversationWaitCancellationAndIndependentConversations(t *testing.T) {
	model := blockingModel{make(chan string, 2), make(chan struct{})}
	outcomes := make(chan AnswerOutcome, 3)
	a, _ := New(model, "m", "s").WithExtensions("", nil, []AnswerHook{{After: func(_ context.Context, o AnswerOutcome) error { outcomes <- o; return nil }}})
	done := make(chan error, 2)
	go func() { _, err := a.Respond(context.Background(), "one", "first"); done <- err }()
	if id := <-model.started; id != "one" {
		t.Fatal(id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Respond(ctx, "one", "waiting"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	go func() { _, err := a.Respond(context.Background(), "two", "independent"); done <- err }()
	if id := <-model.started; id != "two" {
		t.Fatal(id)
	}
	close(model.finish)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(outcomes) != 2 {
		t.Fatal("duplicate or missing terminal hooks")
	}
	if len(a.memory.Conversation("one")) != 2 || len(a.memory.Conversation("two")) != 2 {
		t.Fatal("history crossed conversations")
	}
}

func TestValidationRejectsMalformedPairsAndPlainAssistantHook(t *testing.T) {
	system := openai.Message{Role: "system", Content: "s"}
	for _, id := range []string{"", "duplicate"} {
		calls := []openai.ToolCall{{ID: id}}
		if id != "" {
			calls = append(calls, openai.ToolCall{ID: id})
		}
		msgs := []openai.Message{system, {Role: "assistant", ToolCalls: calls}, {Role: "tool", ToolCallID: id}}
		if err := validatePrepared(system, msgs); err == nil || !strings.Contains(err.Error(), "invalid tool call ID") {
			t.Fatal("malformed pair accepted", err)
		}
	}
	a, _ := New(&fakeLLM{}, "m", "s").WithExtensions("", nil, []AnswerHook{{Before: func(context.Context, Preparation) ([]openai.Message, error) {
		return []openai.Message{{Role: "assistant", Content: "untrusted answer"}}, nil
	}}})
	if _, err := a.Respond(context.Background(), "id", "hi"); err == nil {
		t.Fatal("assistant hook text accepted")
	}
}

func TestInspectionOwnsActualToolSpecifications(t *testing.T) {
	tool := NewTool("test", "test", nil, func(context.Context, struct{}) (string, error) { return "result", nil })
	a, _ := New(&fakeLLM{}, "m", "s", tool).WithExtensions("", nil, []AnswerHook{{Name: "h"}})
	if _, err := a.Respond(context.Background(), "id", "hi"); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.Inspection("id")
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Function.Name != "test" {
		t.Fatal(snapshot)
	}
	snapshot.Tools[0].Function.Parameters[0] = 'x'
	next, _ := a.Inspection("id")
	if next.Tools[0].Function.Parameters[0] == 'x' {
		t.Fatal("tool schema alias")
	}
}
