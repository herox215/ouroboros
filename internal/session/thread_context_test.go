package session

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"ouroboros/internal/agent"
)

// The thread shows what a session said before its question: without it the
// owner sees only a question that refers to the session's conversation.
func TestThreadShowsWhatTheSessionSaidBeforeItsQuestion(t *testing.T) {
	asks := json.RawMessage(`{"question":"Allow rebuild?","options":["Allow once","No"]}`)
	s, gate, local, thread := questionFixture(t, asks)
	s.mu.Lock()
	s.emit(s.get("s1"), Event{Kind: Say, PresentationID: "said", Text: "Two tests fail in the build."})
	s.mu.Unlock()
	ask(t, s, "send s1 follow-up")
	until(t, local, Queued)
	until(t, thread, Handed)
	close(gate)
	question(t, local)
	if _, ok := openQuestion(t, s); !ok {
		t.Fatal("the question was not relayed")
	}
	events := until(t, thread, Asked)
	said := slices.IndexFunc(events, func(e Event) bool {
		return e.Kind == Say && e.Text == "Two tests fail in the build."
	})
	asked := slices.IndexFunc(events, func(e Event) bool { return e.Kind == Asked && !e.Done })
	if said < 0 || asked < 0 || said > asked {
		t.Fatalf("the thread did not show what the session said before its question: %+v", events)
	}
	if err := s.Steer(WithMessageSource(context.Background(), MessageSource{Interface: "tui"}), ThreadID, "Allow once"); err != nil {
		t.Fatal(err)
	}
}

// What a tool says to the owner on its way is shown in the thread as it is
// said: the question that follows it does not say it again.
func TestThreadDoesNotShowWhatTheToolSaidTwice(t *testing.T) {
	asks := json.RawMessage(`{"question":"Allow rebuild?","options":["Allow once","No"]}`)
	const said = "I look at the failing test first."
	gate := make(chan struct{})
	a := dispatcher(make(chan string, 8), func(ctx context.Context, text string) (string, error) {
		if text != "original" {
			return "follow-up handled", nil
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if !agent.Say(ctx, said) {
			return "", errors.New("nobody hears it")
		}
		answer, err := agent.AskOwner(ctx, asks)
		return "permission: " + answer, err
	})
	s := service(t, a)
	_, local, _ := s.Watch(context.Background(), "s1")
	_, thread, _ := s.Watch(context.Background(), ThreadID)
	if err := s.Ask(WithMessageSource(context.Background(), MessageSource{Interface: "tui"}), "s1", "original"); err != nil {
		t.Fatal(err)
	}
	until(t, local, Message)
	ask(t, s, "send s1 follow-up")
	until(t, local, Queued)
	until(t, thread, Handed)
	close(gate)
	question(t, local)
	events := until(t, thread, Asked)
	shown := 0
	for _, e := range events {
		if e.Kind == Say && e.Text == said {
			shown++
		}
	}
	if shown != 1 {
		t.Fatalf("the thread shows what the tool said %d times: %v", shown, kinds(events))
	}
}
