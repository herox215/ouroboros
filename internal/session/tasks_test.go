package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// errTest is the error of a task that fails.
var errTest = errors.New("test error")

// waiting is a task that ends once gate is closed, with answer, or once it
// is stopped.
func waiting(title string, gate <-chan struct{}, answer string, err error) assistant.Task {
	return assistant.Task{Title: title, Run: func(ctx context.Context) (string, error) {
		select {
		case <-gate:
			return answer, err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
}

// texts are what the agent of a fakeAgent is asked, as it is asked it.
type texts chan string

// delegator is an agent that starts tasks for a message "start", answers
// every other message with what it was asked, and tells asked each text.
func delegator(asked texts, tasks ...assistant.Task) *fakeAgent {
	return &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		asked <- text
		if text != "start" {
			return "on " + text, nil
		}
		started, err := assistant.TasksOf(ctx).Start(tasks)
		if err != nil {
			return "", err
		}
		return "started " + numbersOf(started), nil
	}}
}

func numbersOf(ns []int) string {
	s := ""
	for _, n := range ns {
		s += string(rune('0' + n))
	}
	return s
}

// A task runs past the answer that started it, and keeps the node running
// meanwhile: once it ended, the agent is told its report in an answer of
// its own, which the owner sees start with it.
func TestTaskStartsAnAnswer(t *testing.T) {
	gate := make(chan struct{})
	asked := make(texts, 4)
	a := delegator(asked, waiting("look around", gate, "found it", nil))
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "start"); err != nil {
		t.Fatal(err)
	}
	got := until(t, live, Answer)
	if kinds(got) != "message tasks answer" || got[1].Text != "task 1 · look around" || got[2].Text != "started 1" {
		t.Fatalf("events %+v", got)
	}
	<-asked
	if s.Releases() {
		t.Fatal("the node stops while a task runs")
	}
	if err := s.Delete(ctx, "s1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("deleted while a task runs: %v", err)
	}
	close(gate)
	got = until(t, live, Answer)
	if kinds(got) != "tasks task answer" || got[0].Text != "" ||
		got[1].N != 1 || got[1].Remark != "look around" || got[1].Text != "found it" || got[1].Err != "" {
		t.Fatalf("events %+v", got)
	}
	if text := <-asked; text != assistant.TaskReport(1, "look around", "found it", nil) {
		t.Fatalf("asked %q", text)
	}
}

// A task thinks as much as the session it runs in, as the owner set it then.
func TestTaskHasTheEffortOfItsSession(t *testing.T) {
	effort := make(chan string, 1)
	think := assistant.Task{Title: "think", Run: func(ctx context.Context) (string, error) {
		effort <- openai.EffortOf(ctx)
		return "", nil
	}}
	asked := make(texts, 4)
	s := service(t, delegator(asked, think))
	ctx := context.Background()
	s.SetEffort(ctx, "s1", "high")
	s.Ask(ctx, "s1", "start")
	if got := <-effort; got != "high" {
		t.Fatalf("effort %q", got)
	}
}

// A task that ends while an answer is on its way goes into it, as what the
// owner says on the way does: after the tools of its round. A task that
// failed is told so.
func TestTaskGoesIntoTheAnswer(t *testing.T) {
	gate := make(chan struct{})
	steered := make(chan []string, 1)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if _, err := assistant.TasksOf(ctx).Start([]assistant.Task{waiting("fail", gate, "", errTest)}); err != nil {
			return "", err
		}
		close(gate)
		for {
			if texts := agent.Steered(ctx); len(texts) > 0 {
				steered <- texts
				return "took it", nil
			}
			time.Sleep(time.Millisecond)
		}
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "start")
	got := until(t, live, Answer)
	if kinds(got) != "message tasks tasks task answer" || got[3].Err != "test error" || got[3].Text != "" {
		t.Fatalf("events %+v", got)
	}
	if texts := <-steered; len(texts) != 1 || texts[0] != assistant.TaskReport(1, "fail", "", errTest) {
		t.Fatalf("steered %q", texts)
	}
}

// At most assistant.MaxDelegates tasks run at once in a session: more are
// not started, none of them.
func TestTasksAtMost(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	var tasks assistant.Tasks
	took := make(chan struct{})
	var once sync.Once // the reports of the tasks start answers too
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		once.Do(func() {
			tasks = assistant.TasksOf(ctx)
			close(took)
		})
		return "", nil
	}}
	s := service(t, a)
	s.Ask(context.Background(), "s1", "go")
	<-took
	three := []assistant.Task{waiting("a", gate, "", nil), waiting("b", gate, "", nil), waiting("c", gate, "", nil)}
	if started, err := tasks.Start(three); err != nil || numbersOf(started) != "123" {
		t.Fatalf("started %v, %v", started, err)
	}
	if _, err := tasks.Start(three[:2]); err == nil || err.Error() != "3 tasks run in this conversation already, and at most 4 run at once: start more once one ended" {
		t.Fatalf("two more: %v", err)
	}
	if started, err := tasks.Start(three[:1]); err != nil || numbersOf(started) != "4" {
		t.Fatalf("one more: %v, %v", started, err)
	}
}

// Cancel stops the tasks of a session along with its answer, whether one is
// on its way or not: the agent is not told of them, and no answer starts
// for them. A task stopped by Close is not told either.
func TestCancelStopsTheTasks(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	asked := make(texts, 4)
	a := delegator(asked, waiting("a", gate, "", nil), waiting("b", gate, "", nil))
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "start")
	until(t, live, Answer)
	<-asked
	if err := s.Cancel(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	got := until(t, live, Tasks)
	got = append(got, until(t, live, Tasks)...)
	if kinds(got) != "tasks tasks" || got[1].Text != "" {
		t.Fatalf("events %+v", got)
	}
	if err := s.Cancel(ctx, "none"); err != nil {
		t.Fatal(err)
	}
	// Close stops what runs.
	s.Ask(ctx, "s1", "start")
	until(t, live, Answer)
	s.Close()
	select {
	case text := <-asked:
		if text != "start" {
			t.Fatalf("asked %q", text)
		}
	default:
	}
	if len(asked) != 0 {
		t.Fatalf("asked after Close: %q", <-asked)
	}
	// A Service that is closed starts no task.
	var tasks assistant.Tasks
	s2 := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		tasks = assistant.TasksOf(ctx)
		return "", nil
	}})
	_, live2, _ := s2.Watch(ctx, "s1")
	s2.Ask(ctx, "s1", "go")
	until(t, live2, Answer)
	s2.Close()
	if _, err := tasks.Start([]assistant.Task{waiting("a", gate, "", nil)}); !errors.Is(err, ErrStopped) {
		t.Fatalf("closed: %v", err)
	}
}

// What a task reports is not the owner's to take back: Unsteer takes the
// last message of the owner, and leaves the reports queued.
func TestUnsteerLeavesTheReports(t *testing.T) {
	s := service(t, &fakeAgent{})
	s.mu.Lock()
	se := s.get("s1")
	report := queued{text: "report", task: &Event{Kind: Task, N: 1}}
	se.queue = []queued{{text: "mine"}, report}
	s.mu.Unlock()
	ctx := context.Background()
	if text, err := s.Unsteer(ctx, "s1"); text != "mine" || err != nil {
		t.Fatalf("got %q, %v", text, err)
	}
	if text, err := s.Unsteer(ctx, "s1"); text != "" || err != nil {
		t.Fatalf("only a report: %q, %v", text, err)
	}
	if len(se.queue) != 1 || se.queue[0].text != "report" {
		t.Fatalf("queue %+v", se.queue)
	}
	// The next message of the owner comes after it.
	asked := make(texts, 1)
	s.agent = &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		asked <- text
		return "", nil
	}}
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "next")
	got := until(t, live, Answer)
	if kinds(got) != "task message answer" || <-asked != "report\n\nnext" {
		t.Fatalf("events %+v", got)
	}
}

// A report of a task in the archive is shown as the Task it was, not as a
// message of the owner.
func TestReplayedTask(t *testing.T) {
	report := assistant.TaskReport(2, "look", "found it", nil)
	events, msgs := replay([]history.Record{{Role: "user", Content: report}, {Role: "user", Content: "mine"}})
	if len(events) != 2 || events[0].Kind != Task || events[0].N != 2 || events[0].Remark != "look" || events[0].Text != "found it" ||
		events[1].Kind != Message || len(msgs) != 2 || msgs[0].Content != report {
		t.Fatalf("events %+v, msgs %+v", events, msgs)
	}
}

// A task keeps the node from the build its answer asked for until it ended,
// as an answer on its way does.
func TestTaskKeepsTheNodeFromItsBuild(t *testing.T) {
	gate := make(chan struct{})
	task := waiting("look around", gate, "found it", nil)
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		if _, err := assistant.TasksOf(ctx).Start([]assistant.Task{task}); err != nil {
			return "", err
		}
		AskRestart(ctx, "/new/ouro")
		return "started", nil
	}})
	replaced := make(chan string, 2)
	s.Attach(nil, func(exe string) { replaced <- exe })
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "start"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Restart)
	s.List(ctx) // the end of the answer is over once its lock is free
	select {
	case exe := <-replaced:
		t.Fatalf("replaced by %s while a task runs", exe)
	default:
	}
	close(gate)
	select {
	case exe := <-replaced:
		if exe != "/new/ouro" {
			t.Fatalf("replaced by %q", exe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not replaced once the task ended")
	}
}
