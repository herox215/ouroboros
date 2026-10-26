package session

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
)

// A continuation may itself delegate again. Every completed answer is
// reported once; the route is inherited only by work started by that answer.
func TestThreadDelegatedContinuationChain(t *testing.T) {
	ctx := context.Background()
	first, last := make(chan struct{}), make(chan struct{})
	reports := make(texts, 8)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if assistant.ThreadOf(ctx) != nil {
			reports <- text
			return "explained " + text, nil
		}
		switch {
		case text == "start":
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("first", first, "first finding", nil)})
			return "first pending", err
		case strings.Contains(text, "first finding"):
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("last", last, "last finding", nil)})
			return "last pending", err
		default:
			return "final finding", nil
		}
	}}
	s := service(t, a)
	_, main, _ := s.Watch(ctx, ThreadID)
	if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"first pending", "last pending", "final finding"} {
		if i == 1 {
			close(first)
		}
		if i == 2 {
			close(last)
		}
		until(t, main, Answer)
		if text := <-reports; !strings.HasSuffix(text, want) {
			t.Fatalf("report %d: %q", i, text)
		}
	}
	s.answers.Wait()
	noAnswerYet(t, s, reports)
}

func TestThreadDelegatedContinuationErrors(t *testing.T) {
	for _, mode := range []string{"agent_stop", "failed_answer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			gate := make(chan struct{})
			controls := make(chan assistant.Tasks, 1)
			reports := make(texts, 8)
			a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
				if assistant.ThreadOf(ctx) != nil {
					reports <- text
					return "explained " + text, nil
				}
				if text == "start" {
					tasks := assistant.TasksOf(ctx)
					_, err := tasks.Start([]assistant.Task{waiting("work", gate, "finding", nil)})
					controls <- tasks
					return "pending", err
				}
				if mode == "failed_answer" {
					return "", errTest
				}
				return "handled: " + text, nil
			}}
			s := service(t, a)
			_, main, _ := s.Watch(ctx, ThreadID)
			if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			until(t, main, Answer)
			<-reports
			tasks := <-controls
			if mode == "agent_stop" {
				if err := tasks.Stop(1); err != nil {
					t.Fatal(err)
				}
			} else {
				close(gate)
			}
			until(t, main, Answer)
			text := <-reports
			if mode == "agent_stop" && !strings.Contains(text, errStoppedByAgent.Error()) {
				t.Fatalf("missing stop report: %q", text)
			}
			if mode == "failed_answer" && (!strings.Contains(text, "failed.") || !strings.HasSuffix(text, errTest.Error())) {
				t.Fatalf("missing failed answer: %q", text)
			}
			s.answers.Wait()
			noAnswerYet(t, s, reports)
		})
	}
}

// Cancel must also revoke the route of reports already waiting for another
// task, without deleting their data or relaying them as a newer request.
func TestThreadCancelQueuedTaskRoute(t *testing.T) {
	for _, target := range []string{"s1", ThreadID} {
		t.Run(target, func(t *testing.T) {
			ctx := context.Background()
			first, last := make(chan struct{}), make(chan struct{})
			asked := make(texts, 8)
			s := service(t, delegator(asked, waiting("first", first, "kept finding", nil), waiting("last", last, "stale", nil)))
			_, child, _ := s.Watch(ctx, "s1")
			_, main, _ := s.Watch(ctx, ThreadID)
			if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			<-asked
			until(t, main, Answer)
			<-asked
			close(first)
			until(t, child, Tasks)
			if err := s.Cancel(ctx, target); err != nil {
				t.Fatal(err)
			}
			until(t, child, Tasks) // last task stopped; queued data waits for input
			if err := s.Ask(ctx, "s1", "new direct request"); err != nil {
				t.Fatal(err)
			}
			until(t, child, Answer)
			if text := <-asked; !strings.Contains(text, "kept finding") || strings.Contains(text, "stale") {
				t.Fatalf("pending report data: %q", text)
			}
			s.answers.Wait()
			noAnswerYet(t, s, asked)
		})
	}
}

// A report of thread work that ends during a direct answer is not steered
// into it: it waits for an answer of its own, which the thread shows, and
// the thread is not shown the direct answer.
func TestThreadTaskReportWaitsForItsOwnAnswer(t *testing.T) {
	ctx := context.Background()
	gate, resume := make(chan struct{}), make(chan struct{})
	entered := make(chan struct{})
	reports := make(texts, 8)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if assistant.ThreadOf(ctx) != nil {
			reports <- text
			return "explained " + text, nil
		}
		switch text {
		case "start":
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("work", gate, "finding", nil)})
			return "pending", err
		case "status?":
			close(entered)
			<-resume
			if steered := agent.Steered(ctx); len(steered) != 0 {
				return "steered " + steered[0], nil
			}
			return "status ok", nil
		}
		return "complete " + text, nil
	}}
	s := service(t, a)
	_, child, _ := s.Watch(ctx, "s1")
	_, main, _ := s.Watch(ctx, ThreadID)
	if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	until(t, child, Answer)
	until(t, main, Answer)
	<-reports
	if err := s.Ask(ctx, "s1", "status?"); err != nil {
		t.Fatal(err)
	}
	<-entered
	close(gate)
	until(t, child, Tasks)
	close(resume)
	if got := until(t, child, Answer); got[len(got)-1].Text != "status ok" {
		t.Fatalf("direct answer: %+v", got[len(got)-1])
	}
	until(t, main, Answer)
	if text := <-reports; !strings.Contains(text, "complete ") || !strings.Contains(text, "finding") || strings.Contains(text, "status ok") {
		t.Fatalf("thread was told %q", text)
	}
	s.answers.Wait()
	noAnswerYet(t, s, reports)
}

func TestThreadCancelLeavesUnrelatedTasks(t *testing.T) {
	ctx := context.Background()
	first, last := make(chan struct{}), make(chan struct{})
	reports := make(texts, 8)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if assistant.ThreadOf(ctx) != nil {
			reports <- text
			return "explained " + text, nil
		}
		switch text {
		case "start":
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("routed", first, "canceled result", nil)})
			return "pending", err
		case "direct":
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("unrelated", last, "direct finding", nil)})
			return "direct pending", err
		default:
			return text, nil
		}
	}}
	s := service(t, a)
	_, child, _ := s.Watch(ctx, "s1")
	_, main, _ := s.Watch(ctx, ThreadID)
	if _, _, err := s.hand(ctx, "s1", "start", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	until(t, child, Answer)
	until(t, main, Answer)
	<-reports
	if err := s.Ask(ctx, "s1", "direct"); err != nil {
		t.Fatal(err)
	}
	until(t, child, Answer)
	if err := s.Cancel(ctx, ThreadID); err != nil {
		t.Fatal(err)
	}
	until(t, child, Tasks)
	s.mu.Lock()
	tasks := s.get("s1").tasks
	if len(tasks) != 1 || tasks[0].n != 2 || tasks[0].routed {
		t.Errorf("unrelated task was canceled or inherited routing: %+v", tasks)
	}
	if s.get(ThreadID).working != 0 {
		t.Error("canceled work still shown as working for thread")
	}
	s.mu.Unlock()
	close(last)
	got := until(t, child, Answer)
	if text := got[len(got)-1].Text; !strings.Contains(text, "direct finding") || strings.Contains(text, "canceled result") {
		t.Fatalf("wrong task result: %q", text)
	}
	s.answers.Wait()
	noAnswerYet(t, s, reports)
}

// A report that came while the owner stopped the answer does not start an
// answer by itself: it waits for the owner's next message.
func TestCancelHoldsTaskReports(t *testing.T) {
	ctx := context.Background()
	gate, entered := make(chan struct{}), make(chan struct{})
	asked := make(texts, 8)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		switch text {
		case "start":
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("work", gate, "finding", nil)})
			return "pending", err
		case "busy":
			close(entered)
			<-ctx.Done()
			return "", ctx.Err()
		}
		asked <- text
		return "on " + text, nil
	}}
	s := service(t, a)
	_, child, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "start"); err != nil {
		t.Fatal(err)
	}
	until(t, child, Answer)
	if err := s.Ask(ctx, "s1", "busy"); err != nil {
		t.Fatal(err)
	}
	<-entered
	close(gate)
	until(t, child, Tasks)
	if err := s.Cancel(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	until(t, child, Canceled)
	s.answers.Wait()
	noAnswerYet(t, s, asked)
	if err := s.Ask(ctx, "s1", "next"); err != nil {
		t.Fatal(err)
	}
	if text := <-asked; !strings.Contains(text, "finding") || !strings.Contains(text, "next") {
		t.Fatalf("the next answer was asked %q", text)
	}
}
