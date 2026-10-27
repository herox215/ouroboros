package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
)

// noAnswerYet fails if the agent of a session was asked something: s.List
// waits for what holds the lock of s, like the end of a task.
func noAnswerYet(t *testing.T, s *Service, asked texts) {
	t.Helper()
	s.List(context.Background())
	select {
	case text := <-asked:
		t.Fatalf("asked %q", text)
	default:
	}
}

// Tasks run past the answer that started them, and keep no message of the
// owner waiting: the session answers it while they run. The reports wait
// for the last task, and come together in one answer.
func TestTasksRunPastTheAnswer(t *testing.T) {
	first, last := make(chan struct{}), make(chan struct{})
	asked := make(texts, 8)
	s := service(t, delegator(asked, waiting("first", first, "found it", nil), waiting("last", last, "", errTest)))
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "start"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	<-asked
	// The owner is answered while the tasks run.
	if err := s.Steer(ctx, "s1", "quicker, please"); err != nil {
		t.Fatal(err)
	}
	if got := until(t, live, Answer); got[len(got)-1].Text != "on quicker, please" || <-asked != "quicker, please" {
		t.Fatalf("events %+v", got)
	}
	// The report of the first waits for the last.
	close(first)
	until(t, live, Tasks)
	noAnswerYet(t, s, asked)
	close(last)
	got := until(t, live, Answer)
	if kinds(got) != "tasks task task answer" {
		t.Fatalf("events %+v", got)
	}
	want := assistant.TaskReport(1, "first", "found it", nil) + "\n\n" + assistant.TaskReport(2, "last", "", errTest)
	if text := <-asked; text != want {
		t.Fatalf("asked %q", text)
	}
}

// The agent tells the helper of a task that runs what changed, and stops a
// task: its report says so. A task that does not run is neither.
func TestAgentReachesItsTasks(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	listen := assistant.Task{Title: "listen", Run: func(ctx context.Context) (string, error) {
		told := assistant.TaskSteering(ctx)
		for {
			if said := told(); len(said) > 0 {
				return "heard " + said[0].Content, nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}}
	var tasks assistant.Tasks
	asked := make(texts, 4)
	a := delegator(asked, listen, waiting("wait", gate, "", nil))
	run := a.run
	a.run = func(ctx context.Context, text string, observe func(assistant.Step), say func(string)) (string, error) {
		tasks = assistant.TasksOf(ctx)
		return run(ctx, text, observe, say)
	}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "start")
	until(t, live, Answer)
	<-asked
	if err := tasks.Stop(2); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Tell(1, "be quick"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	text := <-asked
	for _, report := range []string{
		assistant.TaskReport(1, "listen", "heard be quick", nil),
		assistant.TaskReport(2, "wait", "", errStoppedByAgent),
	} {
		if !strings.Contains(text, report) {
			t.Fatalf("asked %q, without %q", text, report)
		}
	}
	if err := tasks.Stop(1); err == nil || err.Error() != "task 1 does not run: it ended, and its report comes or came as a message" {
		t.Fatalf("stopped what ended: %v", err)
	}
	if err := tasks.Tell(3, "x"); err == nil {
		t.Fatal("told what never started")
	}
}
