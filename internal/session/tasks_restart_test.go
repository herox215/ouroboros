package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/presentation"
)

// A task that runs holds the node as an answer does. Once a tool asked for a
// new build and the grace is over, the task is stopped as the answers are: it
// ends with this image anyway, and must not hold the new build back until it
// is done.
func TestRestartStopsRunningTask(t *testing.T) {
	gate := make(chan struct{})
	asked := make(texts, 4)
	s := service(t, delegator(asked, waiting("long look", gate, "late", nil)))
	builder := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		return "built", nil
	}})
	Share(s, builder)
	s.node.restartWait = 30 * time.Millisecond
	replaced := make(chan string, 1)
	builder.Attach(nil, func(exe string) { replaced <- exe })
	if err := s.Ask(context.Background(), "work", "start"); err != nil {
		t.Fatal(err)
	}
	<-asked
	if err := builder.Ask(context.Background(), "builder", "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case exe := <-replaced:
		if exe != "/new/ouro" {
			t.Fatalf("replaced by %q", exe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a running task held the new build back")
	}
	s.answers.Wait()
	close(gate)
}

// A task that ends while ouro starts anew is not lost with the queue: its
// report is kept with what the agent remembers of the conversation.
func TestTaskReportSurvivesRestart(t *testing.T) {
	a := &fakeAgent{}
	s := service(t, a)
	s.mu.Lock()
	se := s.get("work")
	p := s.node
	p.mu.Lock()
	p.successor = "/new/ouro"
	s.deliver(se, p, &task{n: 1, title: "review"}, "two findings", nil, 0)
	queued := len(se.queue)
	p.successor = ""
	p.mu.Unlock()
	s.mu.Unlock()
	if queued != 0 {
		t.Errorf("the report waits in the queue, which ends with the process: %d", queued)
	}
	if want := "work: " + assistant.TaskReport(1, "review", "two findings", nil); len(a.notes) != 1 || a.notes[0] != want {
		t.Errorf("noted %q, want %q", a.notes, want)
	}
}

// A task the agent names that ended is told from one that never started, and
// one that ended with a node that started anew names the restart and the
// work it lost.
func TestTaskOfAnEarlierImage(t *testing.T) {
	se := &session{delegated: 2, lost: map[int]string{1: "review"}}
	if err := errNotRunning(se, 3); !strings.Contains(err.Error(), "never started") {
		t.Errorf("a task that never started: %v", err)
	}
	if err := errNotRunning(se, 2); strings.Contains(err.Error(), "started anew") || !strings.Contains(err.Error(), "ended") {
		t.Errorf("a task that ended: %v", err)
	}
	err := errNotRunning(se, 1)
	if !strings.Contains(err.Error(), "started anew") || !strings.Contains(err.Error(), "review") || !strings.Contains(err.Error(), "lost") {
		t.Errorf("a task that ended with the node: %v", err)
	}
	if err := errNotRunning(&session{}, 4); !strings.Contains(err.Error(), "never started") {
		t.Errorf("a task in a session that delegated none: %v", err)
	}
}

// The tasks a session delegates that run are kept with the archive, with their
// number, what they are to do and since when, so that a node that starts anew
// knows what ran. Once one ended, the archive says so.
func TestTasksInFlightAreArchived(t *testing.T) {
	dir := t.TempDir()
	gate := make(chan struct{})
	asked := make(texts, 4)
	s := service(t, delegator(asked, waiting("find the callers", gate, "called twice", nil)))
	s.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	_, live, _ := s.Watch(context.Background(), "s1")
	if err := s.Ask(context.Background(), "s1", "start"); err != nil {
		t.Fatal(err)
	}
	<-asked
	until(t, live, Answer)
	tasks, err := history.Open(dir).Tasks("owner/s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].N != 1 || tasks[0].Title != "find the callers" || tasks[0].Ended != history.TaskRunning || tasks[0].Since.IsZero() {
		t.Fatalf("archived while it runs: %+v", tasks)
	}
	close(gate)
	until(t, live, Answer) // the report of the task
	tasks, err = history.Open(dir).Tasks("owner/s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Ended != history.TaskEnded {
		t.Fatalf("archived after it ended: %+v", tasks)
	}
}

// A node that starts anew tells the session whose tasks ran what they were,
// once, and the next image says that they ended with the node when the agent
// reaches for one.
func TestGoOnTellsOfTasksTheNodeEnded(t *testing.T) {
	dir := t.TempDir()
	if err := history.Open(dir).SetTitle("owner/s1", "start"); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	asked := make(texts, 4)
	first := service(t, delegator(asked, waiting("find the callers", gate, "called twice", nil)))
	first.Archive(history.Open(dir), func(f string, a ...any) { t.Logf(f, a...) }, func(string) string { return "" })
	_, live, _ := first.Watch(context.Background(), "s1")
	if err := first.Ask(context.Background(), "s1", "start"); err != nil {
		t.Fatal(err)
	}
	<-asked
	until(t, live, Answer)
	first.Close() // the node goes away with the task running
	kept, err := history.Open(dir).Tasks("owner/s1")
	if err != nil || len(kept) != 1 || kept[0].Ended != history.TaskRunning {
		t.Fatalf("kept over the restart: %+v, %v", kept, err)
	}
	old := processInstance
	processInstance = presentation.NewID() // the image that starts anew
	defer func() { processInstance = old }()

	told := make(chan string, 4)
	next := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "went on", nil
	}})
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	next.GoOn()
	select {
	case got := <-told:
		for _, want := range []string{"started anew", "task 1 · find the callers", "work is lost", assistant.ToolDelegate} {
			if !strings.Contains(got, want) {
				t.Errorf("told %q, want %q", got, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not told of the tasks that ended with the node")
	}
	next.GoOn()
	select {
	case got := <-told:
		t.Fatalf("it was told twice: %q", got)
	case <-time.After(100 * time.Millisecond):
	}

	next.mu.Lock()
	se := next.get("s1")
	next.mu.Unlock()
	if err := (delegating{s: next, se: se}).Tell(1, "hurry"); err == nil || !strings.Contains(err.Error(), "started anew") {
		t.Errorf("telling a task that ended with the node: %v", err)
	}
	if err := (delegating{s: next, se: se}).Stop(1); err == nil || !strings.Contains(err.Error(), "started anew") {
		t.Errorf("stopping a task that ended with the node: %v", err)
	}
}
