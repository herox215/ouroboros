package agent

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/openai"
	"ouroboros/internal/presentation"
)

func TestPresentationIDsReachHooksAndHistory(t *testing.T) {
	ctx := presentation.New(context.Background())
	llm := &fakeLLM{}
	var doneIDs, eventIDs, hookIDs []string
	provider := ContextProvider{
		Name:    "presentation",
		Prepare: func(_ context.Context, p Preparation) ([]openai.Message, error) { return p.Messages, nil },
		Observe: func(_ context.Context, _ Processing, m openai.Message) error {
			if m.Role == "assistant" {
				eventIDs = append(eventIDs, m.PresentationID)
			}
			return nil
		},
	}
	a, err := New(llm, "m", "s").WithExtensions("presentation", []ContextProvider{provider}, []AnswerHook{{
		Name: "presentation",
		After: func(hookCtx context.Context, outcome AnswerOutcome) error {
			for _, m := range outcome.Messages {
				if m.Role == "assistant" {
					hookIDs = append(hookIDs, m.PresentationID)
					if m.PresentationID == "" || m.PresentationID != presentation.Current(hookCtx) {
						t.Fatalf("hook identity %q, current %q", m.PresentationID, presentation.Current(hookCtx))
					}
				}
			}
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err := a.Run(ctx, "id", "same text", Observer{Call: func(c Call) {
			if c.Done {
				id := presentation.Current(ctx)
				if id == "" || c.Err != nil {
					t.Fatalf("Call done missing identity: %q, %v", id, c.Err)
				}
				doneIDs = append(doneIDs, id)
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(doneIDs) != 2 || doneIDs[0] == doneIDs[1] || len(eventIDs) != 2 || len(hookIDs) != 2 {
		t.Fatalf("done %v, events %v, hooks %v", doneIDs, eventIDs, hookIDs)
	}
	var historyIDs []string
	for _, m := range a.memory.Conversation("id") {
		if m.Role == "assistant" {
			historyIDs = append(historyIDs, m.PresentationID)
		}
	}
	if len(historyIDs) != 2 {
		t.Fatalf("history IDs: %v", historyIDs)
	}
	for i, id := range doneIDs {
		if eventIDs[i] != id || hookIDs[i] != id || historyIDs[i] != id {
			t.Fatalf("identity lost: done %v, events %v, hooks %v, history %v", doneIDs, eventIDs, hookIDs, historyIDs)
		}
	}
}

func TestPresentationTraceEnsuredAndToolMessagesIdentified(t *testing.T) {
	tool := NewTool("test", "test", nil, func(context.Context, struct{}) (string, error) { return "result", nil })
	var hookIDs []string
	a, err := New(&fakeLLM{}, "m", "s", tool).WithExtensions("", nil, []AnswerHook{{
		Name: "presentation",
		After: func(ctx context.Context, o AnswerOutcome) error {
			if presentation.Terminal(ctx) == "" {
				t.Fatal("Run did not ensure a trace")
			}
			for _, m := range o.Messages {
				if m.Role == "assistant" {
					hookIDs = append(hookIDs, m.PresentationID)
				} else if m.PresentationID != "" {
					t.Fatalf("non-LLM message assigned identity: %+v", m)
				}
			}
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Respond(context.Background(), "id", "call test"); err != nil {
		t.Fatal(err)
	}
	if len(hookIDs) != 2 || hookIDs[0] == "" || hookIDs[1] == "" || hookIDs[0] == hookIDs[1] {
		t.Fatalf("tool loop identities: %v", hookIDs)
	}
}

func TestFailedLLMDoesNotAdvancePresentation(t *testing.T) {
	ctx := presentation.New(context.Background())
	previous := presentation.Next(ctx)
	failure := errors.New("failed")
	a := New(&fakeLLM{err: failure}, "m", "s")
	done := false
	_, err := a.Run(ctx, "id", "hi", Observer{Call: func(c Call) {
		if c.Done {
			done = true
			if c.Err != failure || presentation.Current(ctx) != previous {
				t.Fatal("failed completion advanced presentation before Call done")
			}
		}
	}})
	if err != failure || !done || presentation.Current(ctx) != previous {
		t.Fatalf("failed completion: %v, done %v, current %q", err, done, presentation.Current(ctx))
	}
	for _, m := range a.memory.Conversation("id") {
		if m.PresentationID != "" {
			t.Fatalf("failed completion stored an identity: %+v", m)
		}
	}
}
