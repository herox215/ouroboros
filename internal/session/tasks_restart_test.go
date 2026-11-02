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

// A report that ended before the agent took it is kept, and a node that
// starts anew tells it once, as the report of that task and not as a message
// of the owner, marked as coming from before the restart.
func TestTaskReportWaitsForTheNextImage(t *testing.T) {
	dir := t.TempDir()
	done, running := make(chan struct{}), make(chan struct{})
	asked := make(texts, 4)
	first := service(t, delegator(asked, waiting("first work", done, "first finding", nil), waiting("second work", running, "second finding", nil)))
	first.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	ctx := context.Background()
	_, live, _ := first.Watch(ctx, "s1")
	if err := first.Ask(ctx, "s1", "start"); err != nil {
		t.Fatal(err)
	}
	<-asked
	until(t, live, Answer) // the answer that delegated both tasks
	close(done)            // the first ends while the second runs: its report waits
	until(t, live, Tasks)
	var kept []history.Task
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		kept, err = history.Open(dir).Tasks("owner/s1")
		if err == nil && len(kept) == 2 && kept[0].Ended == history.TaskReport && strings.Contains(kept[0].Report, "first finding") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("kept over the restart: %+v, %v", kept, err)
		}
	}
	first.Close() // the node goes away before the agent took the report

	old := processInstance
	processInstance = presentation.NewID() // the image that starts anew
	defer func() { processInstance = old }()

	told := make(chan string, 4)
	next := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "went on", nil
	}})
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	_, nextLive, _ := next.Watch(ctx, "s1")
	next.GoOn()
	got := until(t, nextLive, Task)
	var report *Event
	for i := range got {
		if got[i].Kind == Task {
			report = &got[i]
		}
	}
	if report == nil || report.N != 1 || report.Remark != "first work" || report.Text != "first finding" {
		t.Fatalf("told as the report of the task: %+v in %+v", report, got)
	}
	select {
	case text := <-told:
		for _, want := range []string{"first finding", "before the restart", "second work", "work is lost"} {
			if !strings.Contains(text, want) {
				t.Errorf("told %q, want %q", text, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not told the report that waited")
	}
	next.answers.Wait()
	kept, err = history.Open(dir).Tasks("owner/s1")
	if err != nil || len(kept) != 2 || kept[0].Ended != history.TaskEnded || kept[0].Report != "" || kept[1].Ended != history.TaskRestart {
		t.Fatalf("kept after it was told: %+v, %v", kept, err)
	}
	if pending, err := history.Open(dir).PendingTaskReports(processInstance); err != nil || len(pending) != 0 {
		t.Fatalf("told again at the next start: %+v, %v", pending, err)
	}
	next.GoOn()
	select {
	case text := <-told:
		t.Fatalf("it was told twice: %q", text)
	case <-time.After(100 * time.Millisecond):
	}
}

// A session whose only news from before the restart is the report of a task
// is told that ouro started anew, and the report follows.
func TestGoOnTellsAReportAlone(t *testing.T) {
	dir := t.TempDir()
	report := history.Task{N: 1, Title: "look", Since: time.Now(), Report: assistant.TaskReport(1, "look", "found it", nil)}
	if err := history.Open(dir).SetTaskReport("owner/s1", report, "old"); err != nil {
		t.Fatal(err)
	}
	old := processInstance
	processInstance = presentation.NewID()
	defer func() { processInstance = old }()

	ctx := context.Background()
	told := make(chan string, 4)
	next := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "went on", nil
	}})
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	_, live, _ := next.Watch(ctx, "s1")
	next.GoOn()
	got := until(t, live, Task)
	for _, e := range got {
		if e.Kind == Message && (!strings.Contains(e.Text, "[Not from the owner:") || !strings.Contains(e.Text, "before the restart")) {
			t.Fatalf("not marked as coming from before the restart: %+v", e)
		}
	}
	select {
	case text := <-told:
		for _, want := range []string{"[Not from the owner:", "before the restart", "task 1 · look", "found it"} {
			if !strings.Contains(text, want) {
				t.Errorf("told %q, want %q", text, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not told the report alone")
	}
	next.answers.Wait()
	if tasks, err := history.Open(dir).Tasks("owner/s1"); err != nil || len(tasks) != 1 || tasks[0].Ended != history.TaskEnded {
		t.Fatalf("kept after it was told: %+v, %v", tasks, err)
	}
}
