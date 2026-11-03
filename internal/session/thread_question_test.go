package session

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// The thread itself asks the owner too: a command it runs on its own, like a
// change of the model, needs their consent, and what they answer in the
// thread is its answer, not a handoff to a session.
func TestThreadAsksItsOwnQuestion(t *testing.T) {
	asks := json.RawMessage(`{"question":"Change the model to new?","options":["Apply change","Reject change"],"recommended":"Apply change"}`)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if assistant.ThreadOf(ctx) == nil {
			return "pong:" + text, nil
		}
		answer, err := agent.AskOwner(ctx, asks)
		return "model: " + answer, err
	}}
	s := service(t, a)
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, ThreadID, "take the new model"); err != nil {
		t.Fatal(err)
	}
	if got := told(until(t, thread, Asked)); !slices.Contains(got, "asked Change the model to new?") {
		t.Fatalf("got %q", got)
	}
	if err := s.Steer(WithMessageSource(ctx, MessageSource{Interface: "tui"}), ThreadID, "Apply change"); err != nil {
		t.Fatal(err)
	}
	if got := told(until(t, thread, Answer)); !same(got, "answered Apply change", "answer model: Apply change") {
		t.Fatalf("got %q", got)
	}
}

// What the thread says before it asks the owner is shown right before the
// question, as the question rests on it: the thread shows nothing else of
// what its agent says on the way.
func TestThreadShowsWhatItSaysBeforeItAsks(t *testing.T) {
	asks := json.RawMessage(`{"question":"Soll ich daraus ein Ticket schreiben?"}`)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), say func(string)) (string, error) {
		if assistant.ThreadOf(ctx) == nil {
			return "pong:" + text, nil
		}
		say("Der Text fehlt, weil der Thread still ist.")
		answer, err := agent.AskOwnerReply(ctx, asks)
		return "answer: " + answer.Text, err
	}}
	s := service(t, a)
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, ThreadID, "warum fehlt der Text"); err != nil {
		t.Fatal(err)
	}
	if got := words(until(t, thread, Asked)); !same(got, "message warum fehlt der Text", "say Der Text fehlt, weil der Thread still ist.", "asked Soll ich daraus ein Ticket schreiben?") {
		t.Fatalf("got %q", got)
	}
}

// The thread keeps quiet where no question follows what its agent said: its
// words on the way are not shown.
func TestThreadKeepsQuietWithoutAQuestion(t *testing.T) {
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), say func(string)) (string, error) {
		if assistant.ThreadOf(ctx) == nil {
			return "pong:" + text, nil
		}
		say("Ich sehe mir das an.")
		return "Fertig.", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, ThreadID, "schau mal"); err != nil {
		t.Fatal(err)
	}
	if got := words(until(t, thread, Answer)); !same(got, "message schau mal", "answer Fertig.") {
		t.Fatalf("got %q", got)
	}
}

// words are what of events the chat shows of the thread's words and
// questions: what it said is told, and with it the question it rests on.
func words(events []Event) []string {
	var got []string
	for _, e := range events {
		switch e.Kind {
		case Message, Answer, Say:
			got = append(got, string(e.Kind)+" "+e.Text)
		case Asked:
			if e.Done {
				got = append(got, "answered "+e.Text)
			} else {
				got = append(got, "asked "+e.AsQuestion().Question)
			}
		}
	}
	return got
}

// A question is kept with the call that asked it, and so are the words of
// the thread that led to it: loaded from the archive, the chat shows what the
// question rests on before the question, and the answer after it.
func TestArchivedQuestionKeepsWhatItRestsOn(t *testing.T) {
	records := []history.Record{
		{Role: "user", Content: "warum fehlt der Text"},
		{Role: "assistant", Content: "Der Text fehlt, weil der Thread still ist.",
			ToolCalls: []history.Call{{ID: "c1", Name: assistant.ToolUserQuestion, Arguments: `{"question":"Ticket?"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: `{"answer":"Ja","source":{"interface":"tui"}}`},
		{Role: "assistant", Content: "Steht."},
	}
	events, _ := replay(records)
	if got := words(quiet(events)); !same(got, "message warum fehlt der Text",
		"say Der Text fehlt, weil der Thread still ist.", "asked Ticket?", "answered Ja", "answer Steht.") {
		t.Fatalf("got %q", got)
	}
}

// Words of the thread that no question follows stay untold when it is loaded
// from the archive too, and so do its calls.
func TestArchivedThreadKeepsQuietWithoutAQuestion(t *testing.T) {
	records := []history.Record{
		{Role: "user", Content: "schau mal"},
		{Role: "assistant", Content: "Ich sehe mir das an.",
			ToolCalls: []history.Call{{ID: "c2", Name: "read", Arguments: `{"path":"a"}`}}},
		{Role: "tool", ToolCallID: "c2", Content: "ok"},
		{Role: "assistant", Content: "Fertig."},
	}
	events, _ := replay(records)
	if got := words(quiet(events)); !same(got, "message schau mal", "answer Fertig.") {
		t.Fatalf("got %q", got)
	}
}
