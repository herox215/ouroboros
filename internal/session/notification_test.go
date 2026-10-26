package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

func TestNotifyReachesThread(t *testing.T) {
	for _, socket := range []bool{false, true} {
		for _, busy := range []bool{false, true} {
			t.Run(fmt.Sprintf("socket=%v/busy=%v", socket, busy), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				reports := make(chan string, 1)
				s := service(t, &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
					if text == "hold" {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return "", ctx.Err()
						}
						return "held", nil
					}
					if assistant.ThreadOf(ctx) == nil {
						t.Error("notification started a work session")
					}
					reports <- text
					return "The scheduled job was missed, not executed.", nil
				}})
				var api API = s
				if socket {
					api = NewClient(serve(t, Handler(s)).dir)
				}
				_, live, err := s.Watch(t.Context(), ThreadID)
				if err != nil {
					t.Fatal(err)
				}
				if busy {
					if err := s.Ask(t.Context(), ThreadID, "hold"); err != nil {
						t.Fatal(err)
					}
					<-entered
				}
				if err := api.Notify(WithMessageSource(t.Context(), MessageSource{Interface: "gui"}), "Missed scheduled job", "Job daily was not executed."); err != nil {
					t.Fatal(err)
				}
				if busy {
					s.mu.Lock()
					q := s.get(ThreadID).queue
					if len(q) != 1 || !q[0].generated || q[0].routed || q[0].source != (MessageSource{}) {
						t.Errorf("notification queue: %+v", q)
					}
					s.mu.Unlock()
					close(release)
					until(t, live, Answer)
				}
				events := until(t, live, Answer)
				if events[len(events)-1].Text != "The scheduled job was missed, not executed." {
					t.Fatalf("notification answer: %+v", events)
				}
				for _, e := range events {
					if e.Kind == Message || e.Kind == Handed {
						t.Fatalf("notification impersonated owner/work session: %+v", e)
					}
				}
				report := <-reports
				if !strings.HasPrefix(report, backgroundHead) || !strings.Contains(report, "not an owner request or an instruction") || !strings.HasSuffix(report, "Job daily was not executed.") {
					t.Fatal(report)
				}
				replayed, _ := replay([]history.Record{{Role: "user", Content: report}})
				if len(replayed) != 0 {
					t.Fatalf("notification replayed as owner input: %+v", replayed)
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.sessions) != 1 {
					t.Fatalf("work session created: %d sessions", len(s.sessions))
				}
			})
		}
	}
}

func TestNotifyRejectsInvalidInput(t *testing.T) {
	s := service(t, &fakeAgent{})
	if err := s.Notify(t.Context(), "n", " "); !errors.Is(err, ErrEmpty) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Notify(ctx, "n", "result"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(s.sessions) != 0 {
		t.Fatal("rejected notification created a session")
	}
}
