package session

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
)

// Exercise the full thread -> session -> task -> session -> thread path. A
// follow-up and a new thread topic must not consume the task's return address.
func TestThreadDelegatedContinuation(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		name := "single"
		if multiple {
			name = "multiple_with_failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			first, last := make(chan struct{}), make(chan struct{})
			reports := make(texts, 8)
			ids := make(chan string, 1)
			tasks := []assistant.Task{waiting("inspect XYZ", first, "XYZ finding", nil)}
			if multiple {
				tasks = append(tasks, waiting("check XYZ", last, "", errTest))
			}
			a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
				if thread := assistant.ThreadOf(ctx); thread != nil {
					if text == "XYZ" {
						id, err := thread.Start(ctx, "inspect XYZ", "")
						if err != nil {
							return "", err
						}
						ids <- id
						return "accepted XYZ", nil
					}
					if strings.HasPrefix(text, backgroundHead) {
						reports <- text
						return "contextualized: " + text, nil
					}
					return "talking about AAA", nil
				}
				if text == "inspect XYZ" {
					if _, err := assistant.TasksOf(ctx).Start(tasks); err != nil {
						return "", err
					}
					return "running in background", nil
				}
				if text == "status?" {
					return "still running", nil
				}
				return "complete result: " + text, nil
			}}
			s := service(t, a)
			_, main, _ := s.Watch(ctx, ThreadID)
			ask(t, s, "XYZ")
			id := <-ids
			// The relay may be queued behind the initial thread acknowledgement.
			until(t, main, Answer)
			until(t, main, Answer)
			if report := <-reports; !strings.HasSuffix(report, "running in background") {
				t.Fatalf("intermediate: %q", report)
			}
			_, child, _ := s.Watch(ctx, id)
			if err := s.Steer(ctx, id, "status?"); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			ask(t, s, "AAA")
			if got := until(t, main, Answer); got[len(got)-1].Text != "talking about AAA" {
				t.Fatalf("topic change: %+v", got)
			}
			close(first)
			if multiple {
				until(t, child, Tasks)
				noAnswerYet(t, s, reports)
				close(last)
			}
			got := until(t, main, Answer)
			result := got[len(got)-1].Text
			if !strings.Contains(result, "complete result:") || !strings.Contains(result, "XYZ finding") {
				t.Fatalf("missing final result: %q", result)
			}
			for _, e := range got {
				if e.Kind == Message {
					t.Fatalf("result shown as owner input: %+v", e)
				}
			}
			report := <-reports
			if !strings.Contains(report, "inspect XYZ") || multiple && !strings.Contains(report, errTest.Error()) {
				t.Fatalf("wrong report: %q", report)
			}
			noAnswerYet(t, s, reports)
			// Once the continuation is reported, ordinary session input is not routed.
			if err := s.Steer(ctx, id, "status?"); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer) // final task answer
			until(t, child, Answer) // status answer
			s.answers.Wait()
			noAnswerYet(t, s, reports)
		})
	}
}

// A task must carry its own routing bit, never borrow that of a newer answer.
func TestTaskRoutingBelongsToOriginatingAnswer(t *testing.T) {
	for _, routed := range []bool{false, true} {
		name := "unrouted_task_new_routed_answer"
		if routed {
			name = "routed_task_new_direct_answer"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			gate := make(chan struct{})
			asked := make(texts, 8)
			s := service(t, delegator(asked, waiting("old work", gate, "old result", nil)))
			_, child, _ := s.Watch(ctx, "s1")
			_, main, _ := s.Watch(ctx, ThreadID)
			if routed {
				if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			} else if err := s.Ask(ctx, "s1", "start"); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			<-asked
			if routed {
				until(t, main, Answer)
				<-asked
			}
			if routed {
				if err := s.Ask(ctx, "s1", "new direct request"); err != nil {
					t.Fatal(err)
				}
			} else if _, _, err := s.hand(ctx, "s1", "new routed request", nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			<-asked
			if !routed {
				until(t, main, Answer)
				<-asked
			}
			close(gate)
			until(t, child, Answer)
			if text := <-asked; !strings.Contains(text, "old result") {
				t.Fatalf("task report: %q", text)
			}
			if routed {
				got := until(t, main, Answer)
				if !strings.Contains(got[len(got)-1].Text, "old result") {
					t.Fatalf("lost original route: %+v", got)
				}
				<-asked
			}
			s.answers.Wait()
			noAnswerYet(t, s, asked)
		})
	}
}

func TestThreadDelegatedContinuationCanceled(t *testing.T) {
	for _, target := range []string{"s1", ThreadID} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			gate := make(chan struct{})
			asked := make(texts, 8)
			s := service(t, delegator(asked, waiting("work", gate, "stale result", nil)))
			_, child, _ := s.Watch(ctx, "s1")
			_, main, _ := s.Watch(ctx, ThreadID)
			if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			<-asked
			until(t, main, Answer)
			<-asked
			if err := s.Cancel(ctx, target); err != nil {
				t.Fatal(err)
			}
			until(t, child, Tasks)
			noAnswerYet(t, s, asked)
			if _, _, err := s.hand(ctx, "s1", "new request", nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			if text := <-asked; text != "new request" {
				t.Fatalf("stale task resumed new request: %q", text)
			}
			until(t, main, Answer)
			<-asked
			s.mu.Lock()
			for _, e := range s.get("s1").events {
				if e.Kind == Task {
					t.Errorf("canceled task reported: %+v", e)
				}
			}
			s.mu.Unlock()
			s.answers.Wait()
			noAnswerYet(t, s, asked)
		})
	}
}
