package session

import (
	"strings"
	"testing"

	"ouroboros/internal/assistant"
)

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
