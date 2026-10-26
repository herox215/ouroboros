package session

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"ouroboros/internal/agent"
)

// questionFixture starts an answer in s1 that asks asks once the returned
// gate is closed, and follow-ups that are answered at once.
func questionFixture(t *testing.T, asks json.RawMessage) (*Service, chan struct{}, <-chan Event, <-chan Event) {
	t.Helper()
	ctx := context.Background()
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
		answer, err := agent.AskOwner(ctx, asks)
		return "permission: " + answer, err
	})
	s := service(t, a)
	_, local, _ := s.Watch(ctx, "s1")
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, "s1", "original"); err != nil {
		t.Fatal(err)
	}
	until(t, local, Message)
	return s, gate, local, thread
}

// openQuestion is the question the thread asks and has not seen answered.
// Watch takes the service lock after ask emitted the local question and
// attempted its relay, so absence is deterministic, not a timeout.
func openQuestion(t *testing.T, s *Service) (Event, bool) {
	t.Helper()
	past, _, err := s.Watch(context.Background(), ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	open := map[string]Event{}
	for _, e := range past {
		if e.Kind == Asked && !e.Done {
			open[e.PresentationID] = e
		} else if e.Kind == Asked {
			delete(open, e.PresentationID)
		}
	}
	for _, e := range open {
		return e, true
	}
	return Event{}, false
}

// A routed follow-up can wait behind a tool that has not consumed steering
// yet. Its permission question must not leave the thread waiting blindly.
func TestThreadRelaysQuestionBeforeQueuedHandoffIsConsumed(t *testing.T) {
	asks := json.RawMessage(`{"question":"Allow rebuild?","options":["Allow once","No"]}`)
	t.Run("local follow-up", func(t *testing.T) {
		s, gate, local, _ := questionFixture(t, asks)
		if err := s.Steer(context.Background(), "s1", "follow-up"); err != nil {
			t.Fatal(err)
		}
		close(gate)
		question(t, local)
		if _, ok := openQuestion(t, s); ok {
			t.Fatal("a local follow-up leaked a question into the thread")
		}
		if err := s.Steer(context.Background(), "s1", "No"); err != nil {
			t.Fatal(err)
		}
		until(t, local, Answer)
	})
	t.Run("thread follow-up", func(t *testing.T) {
		s, gate, local, thread := questionFixture(t, asks)
		ask(t, s, "send s1 follow-up")
		until(t, local, Queued)
		until(t, thread, Handed)
		close(gate)
		question(t, local)
		q, ok := openQuestion(t, s)
		if !ok {
			t.Fatal("queued thread handoff did not relay the permission question")
		}
		if string(q.Args) != string(asks) || q.PresentationID == "" {
			t.Fatalf("relayed question: %+v", q)
		}
		if err := s.Steer(WithMessageSource(context.Background(), MessageSource{Interface: "tui"}), ThreadID, "Allow once"); err != nil {
			t.Fatal(err)
		}
		localDone := until(t, local, Asked)
		if e := localDone[len(localDone)-1]; !e.Done || e.Text != "Allow once" {
			t.Fatalf("local completion: %+v", e)
		}
		for {
			events := until(t, thread, Asked)
			e := events[len(events)-1]
			if !e.Done {
				continue
			}
			if e.PresentationID != q.PresentationID || e.Text != "Allow once" {
				t.Fatalf("thread completion: %+v", e)
			}
			break
		}
		// The owner approved in the thread: it shows what came of it.
		for {
			events := until(t, thread, Answer)
			if events[len(events)-1].Text == "permission: Allow once" {
				break
			}
		}
	})
}

// A question the thread relays only for a message it handed on is taken
// back once that message no longer waits for the thread: the owner took it
// back, or stopped the thread. The session still waits for its answer.
func TestThreadWithdrawsQuestionWithoutHandoff(t *testing.T) {
	asks := json.RawMessage(`{"question":"Allow rebuild?","options":["Allow once","No"]}`)
	for _, c := range []struct {
		name string
		stop func(s *Service) error
	}{
		{"unsteered", func(s *Service) error {
			_, err := s.Unsteer(context.Background(), "s1")
			return err
		}},
		{"thread stopped", func(s *Service) error { return s.Cancel(context.Background(), ThreadID) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, gate, local, thread := questionFixture(t, asks)
			ask(t, s, "send s1 follow-up")
			until(t, local, Queued)
			until(t, thread, Handed)
			close(gate)
			question(t, local)
			if _, ok := openQuestion(t, s); !ok {
				t.Fatal("the question was not relayed")
			}
			if err := c.stop(s); err != nil {
				t.Fatal(err)
			}
			if _, ok := openQuestion(t, s); ok {
				t.Fatal("the thread still asks the question")
			}
			s.mu.Lock()
			waiting := s.sessions["s1"].asked != nil
			routed := slices.ContainsFunc(s.sessions["s1"].queue, func(m queued) bool { return m.routed })
			s.mu.Unlock()
			if !waiting || routed {
				t.Fatalf("s1 waits %v, routed message queued %v", waiting, routed)
			}
			if err := s.Steer(context.Background(), "s1", "No"); err != nil {
				t.Fatal(err)
			}
			until(t, local, Answer)
		})
	}
}
