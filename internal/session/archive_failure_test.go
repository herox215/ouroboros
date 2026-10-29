package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// fileArchive is the path of an archive that cannot be, as a file holds its
// place: whatever is kept there fails.
func fileArchive(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A session that goes on after an interrupted answer and lost its tasks is
// told both. Of a session whose first task cannot be read, that is logged,
// and so is nothing of what the owner wrote last when it cannot be read.
func TestGoOnWithUnreadableRecords(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	for _, id := range []string{"s1", "s2"} {
		if err := arch.Append(history.Who{Session: id}, []openai.Message{{Role: "user", Content: "the first task"}, {Role: "assistant", Content: "done"}, {Role: "user", Content: "the next one"}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := arch.BeginAnswer(history.Owner(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := arch.SetTasks("owner/s1", []history.Task{{N: 1, Title: "find the callers", Since: time.Now()}}, "an image that is gone"); err != nil {
		t.Fatal(err)
	}
	// The first record of s2 is broken, and a later one of s1.
	breakArchive(t, arch, dir, "UPDATE history_records SET data='not JSON' WHERE (session_id='owner/s1' AND ordinal=3) OR (session_id='owner/s2' AND ordinal=1)")
	told := make(chan string, 4)
	s := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "went on", nil
	}})
	l := &logs{}
	s.Archive(arch, l.log, func(string) string { return "" })
	s.GoOn()
	got := map[string]bool{}
	for range 2 {
		select {
		case text := <-told:
			got[text] = true
			if strings.Contains(text, "find the callers") {
				for _, want := range []string{interruptedText, "\n\nThe tasks this conversation had delegated ended with the node, too", "Its first task, as the owner put it:\nthe first task"} {
					if !strings.Contains(text, want) {
						t.Errorf("told %q, want %q", text, want)
					}
				}
				if strings.Contains(text, "What the owner wrote last") {
					t.Errorf("told what cannot be read: %q", text)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("told only %v", got)
		}
	}
	if !l.has("where the archive of the session owner/s1 ends cannot be read") {
		t.Errorf("log %q", l.lines)
	}
	if !l.has("the first task of the session owner/s2 cannot be read") {
		t.Errorf("log %q", l.lines)
	}
}

// What the archive cannot keep of an answer, its model, the tasks of a
// session and the report of a task, is logged, and the session goes on.
func TestArchiveCannotKeep(t *testing.T) {
	a := &fakeAgent{model: "m1", note: errors.New("no note")}
	s, _, l := archived(t, a, fileArchive(t), time.Now())
	asked(t, s, "s1", "hi")
	s.mu.Lock()
	se := s.get("s1")
	s.keepTasks(se, &task{n: 1, title: "review"})
	p := s.node
	p.mu.Lock()
	p.successor = "/new/ouro"
	s.deliver(se, p, &task{n: 2, title: "review"}, "two findings", nil, 0)
	p.successor = ""
	p.mu.Unlock()
	s.mu.Unlock()
	for _, want := range []string{
		"the model of the session s1 cannot be kept",
		"the tasks of the session s1 cannot be kept",
		"the report of task 2 of the session s1 cannot be kept: no note",
	} {
		if !l.has(want) {
			t.Errorf("not logged: %q in %q", want, l.lines)
		}
	}
}

// A question parked at the end of an answer that can be neither noted nor
// marked to go on is logged.
func TestParkedQuestionNotKept(t *testing.T) {
	var s *Service
	s, _, l := archived(t, &fakeAgent{note: errors.New("no note"), run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		s.mu.Lock()
		s.sessions["s1"].parked = json.RawMessage(`{"question":"Allow?"}`)
		s.mu.Unlock()
		return "parked", nil
	}}, fileArchive(t), time.Now())
	asked(t, s, "s1", "hi")
	s.answers.Wait()
	for _, want := range []string{"the pending question of session s1 cannot be kept: no note", "the pending question of session s1 cannot be resumed"} {
		if !l.has(want) {
			t.Errorf("not logged: %q in %q", want, l.lines)
		}
	}
}

// While tasks run, an answer takes the messages of the owner, and the
// reports of tasks that ended wait for the last one.
func TestReadyKeepsReportsWhileTasksRun(t *testing.T) {
	report := queued{task: &Event{}}
	se := &session{tasks: []*task{{n: 1}}, queue: []queued{report, {text: "more"}}}
	if got := ready(se); len(got) != 1 || got[0].text != "more" {
		t.Fatalf("ready %+v", got)
	}
	if len(se.queue) != 1 || se.queue[0].task != report.task {
		t.Fatalf("waits %+v", se.queue)
	}
}

// What a tool says on the way to an answer the thread handed on is heard in
// the thread too.
func TestSayingReachesThread(t *testing.T) {
	var s *Service
	s = service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		s.mu.Lock()
		s.sessions["s1"].routed = true
		s.mu.Unlock()
		agent.Say(ctx, "on my way")
		return "done", nil
	}})
	_, thread, _ := s.Watch(t.Context(), ThreadID)
	asked(t, s, "s1", "hi")
	for _, e := range until(t, thread, Say) {
		if e.Kind == Say && e.Text != "on my way" {
			t.Fatalf("said %+v", e)
		}
	}
}
