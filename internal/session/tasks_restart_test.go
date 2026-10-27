package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
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

// A task the agent names that this image never started was ended by the
// restart: the error says so, and where to find what it was to do.
func TestTaskOfAnEarlierImage(t *testing.T) {
	se := &session{delegated: 2}
	if err := errNotRunning(se, 3); !strings.Contains(err.Error(), "before ouro started anew") || !strings.Contains(err.Error(), assistant.ToolDelegate) {
		t.Errorf("a task of an earlier image: %v", err)
	}
	if err := errNotRunning(se, 2); strings.Contains(err.Error(), "started anew") {
		t.Errorf("a task that ended: %v", err)
	}
}
