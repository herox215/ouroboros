package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ouroboros/internal/openai"
)

// fakeLLM records the last request and echoes the last message.
type fakeLLM struct {
	model string
	msgs  []openai.Message
	err   error
}

func (f *fakeLLM) Chat(_ context.Context, model string, msgs []openai.Message) (string, error) {
	f.model, f.msgs = model, msgs
	if f.err != nil {
		return "", f.err
	}
	return "echo " + msgs[len(msgs)-1].Content, nil
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
