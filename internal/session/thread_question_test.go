package session

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"ouroboros/internal/agent"
)

// A routed follow-up can wait behind a tool that has not consumed steering
// yet. Its permission question must not leave the thread waiting blindly.
func TestThreadRelaysQuestionBeforeQueuedHandoffIsConsumed(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "local follow-up", true: "thread follow-up"}[routed], func(t *testing.T) {
			ctx := context.Background()
			gate := make(chan struct{})
			asks := json.RawMessage(`{"question":"Allow rebuild?","options":["Allow once","No"]}`)
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
			if routed {
				ask(t, s, "send s1 follow-up")
				until(t, local, Queued)
				until(t, thread, Handed)
			} else if err := s.Steer(ctx, "s1", "follow-up"); err != nil {
				t.Fatal(err)
			}
			close(gate)
			question(t, local)
			// Watch takes the service lock after ask emitted the local question
			// and attempted its relay, so absence is deterministic, not a timeout.
			past, _, err := s.Watch(ctx, ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(past, func(e Event) bool { return e.Kind == Asked && !e.Done })
			if !routed {
				if i >= 0 {
					t.Fatal("a local follow-up leaked a question into the thread")
				}
				if err := s.Steer(ctx, "s1", "No"); err != nil {
					t.Fatal(err)
				}
				until(t, local, Answer)
				return
			}
			if i < 0 {
				t.Fatal("queued thread handoff did not relay the permission question")
			}
			q := past[i]
			if string(q.Args) != string(asks) || q.PresentationID == "" {
				t.Fatalf("relayed question: %+v", q)
			}
			if err := s.Steer(WithMessageSource(ctx, MessageSource{Interface: "tui"}), ThreadID, "Allow once"); err != nil {
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
			until(t, local, Answer)
		})
	}
}
