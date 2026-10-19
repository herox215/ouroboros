package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/openai"
	"ouroboros/internal/presentation"
)

// An answer keeps what its tools put there, from one call to the next, and
// has a context that ends with it, after the hooks were told how it ended.
func TestAnswer(t *testing.T) {
	if AnswerOf(context.Background()) != nil {
		t.Fatal("no answer outside of one")
	}
	var answers []*Answer
	made := 0
	tool := Tool{Name: "shout", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		a := AnswerOf(ctx)
		answers = append(answers, a)
		v := a.Value("count", func() any { made++; return new(int) }).(*int)
		*v++
		if a.Context().Err() != nil {
			t.Error("the answer is not over yet")
		}
		return fmt.Sprint(*v), nil
	}}
	calls := 0
	llm := llmFunc(func(msgs []openai.Message) openai.Message {
		if calls++; calls%3 == 0 {
			return openai.Message{Content: "counted " + msgs[len(msgs)-1].Content}
		}
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: fmt.Sprint("c", calls), Function: openai.FunctionCall{Name: "shout", Arguments: `{}`}}}}
	})
	ended := errors.New("not told")
	hook := AnswerHook{Name: "h", After: func(ctx context.Context, _ AnswerOutcome) error {
		ended = ctx.Err()
		return nil
	}}
	a, err := New(llm, "gpt", "sys", tool).WithExtensions("", nil, []AnswerHook{hook})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := a.Respond(context.Background(), "c1", "go"); err != nil || got != "counted 2" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if len(answers) != 4 || answers[0] != answers[1] || answers[2] != answers[3] || answers[0] == answers[2] || made != 2 {
		t.Fatalf("answers %v, made %d", answers, made)
	}
	if ended != nil {
		t.Fatalf("the hook was told with a context that had ended: %v", ended)
	}
	if answers[0].Context().Err() == nil || answers[2].Context().Err() == nil {
		t.Fatal("the context of an answer ends with it")
	}
}

// A note is remembered without an answer, and the next answer of its
// conversation starts with it. The hooks are told, with what it was presented
// as, and only the last few notes are kept.
func TestNote(t *testing.T) {
	llm := &fakeLLM{}
	var told []string
	boom := errors.New("boom")
	hooks := []AnswerHook{
		{Name: "before", Before: func(context.Context, Preparation) ([]openai.Message, error) { return nil, nil }},
		{Name: "archive", After: func(_ context.Context, o AnswerOutcome) error {
			told = append(told, fmt.Sprint(o.Conversation.ID, o.Conversation.Peer, o.Conversation.Origin.PublicKey, len(o.Messages), o.Messages[0].Content, o.Messages[0].PresentationID, o.Reply, o.Err))
			if o.Messages[0].Content == "bad" {
				return boom
			}
			return nil
		}},
	}
	a, err := New(llm, "gpt", "sys").WithExtensions("", nil, hooks)
	if err != nil {
		t.Fatal(err)
	}
	ctx := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "luna"})
	if err := a.Note(presentation.As(ctx, "shown"), "c1", "thanks"); err != nil {
		t.Fatal(err)
	}
	if err := a.Note(ctx, "c1", "bad"); !errors.Is(err, boom) || err.Error() != "answer hook archive after: boom" {
		t.Fatalf("a hook that fails: %v", err)
	}
	if len(told) != 2 || told[0] != "c1trueluna1thanksshown<nil>" || told[1] != "c1trueluna1bad<nil>" {
		t.Fatalf("told %q", told)
	}
	if a.memory.history("c1") != nil || llm.msgs != nil {
		t.Fatal("a note is no answer")
	}
	if got, err := a.Respond(ctx, "c1", "hi"); err != nil || got != "echo hi" {
		t.Fatalf("got %q, %v", got, err)
	}
	var said []string
	for _, m := range a.memory.history("c1") {
		said = append(said, m.Role+" "+m.Content)
	}
	if strings.Join(said, "|") != "user thanks|user bad|user hi|assistant echo hi" {
		t.Fatalf("remembered %q", said)
	}
	if a.Respond(ctx, "c1", "again"); len(a.memory.history("c1")) != 6 {
		t.Fatalf("the notes are read once: %d messages", len(a.memory.history("c1")))
	}
	if a.memory.noted("nobody") != nil {
		t.Fatal("a conversation that is not known has no notes")
	}
	// Only the last notes are kept, and notes do not keep more conversations
	// than there may be.
	for i := range maxNotes + 2 {
		a.memory.note("many", openai.Message{Content: fmt.Sprint(i)})
	}
	if notes := a.memory.noted("many"); len(notes) != maxNotes || notes[0].Content != "2" || notes[maxNotes-1].Content != fmt.Sprint(maxNotes+1) {
		t.Fatalf("%d notes, from %q", len(notes), notes[0].Content)
	}
	for i := range maxContexts + 1 {
		a.memory.note(fmt.Sprint("n", i), openai.Message{Content: "x"})
	}
	if len(a.memory.conversations) != maxContexts || a.memory.noted("n0") != nil || len(a.memory.noted(fmt.Sprint("n", maxContexts))) != 1 {
		t.Fatalf("%d conversations", len(a.memory.conversations))
	}
}
