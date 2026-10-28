package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The state of a session tells the question it waits for, what it did last
// when no tool runs, its jobs, tasks and the messages that wait, and how its
// last answer ended.
func TestStateTellsEverything(t *testing.T) {
	s := service(t, &fakeAgent{})
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	since := now.Add(-time.Minute)
	at := func(seconds int) time.Time { return since.Add(time.Duration(seconds) * time.Second) }
	se := &session{
		id:     "s1-of-the-owner",
		cancel: func() {},
		since:  since,
		asked:  make(chan queued),
		events: []Event{
			{Kind: Message, Text: "Fix the build", At: at(-10)},
			{Kind: Failed, Text: "it broke", At: at(-5)},
			{Kind: Asked, Args: json.RawMessage(`{"question":"Push it?"}`), At: at(1)},
			{Kind: Step, CallID: "c1", Tool: "bash", At: at(2)},
			{Kind: Step, CallID: "c1", Tool: "bash", Done: true, At: at(3)},
			{Kind: Say, Text: "running the tests", At: at(4)},
		},
		tasks: []*task{{n: 1, title: "find the callers"}},
		queue: []queued{{task: &Event{}}, {generated: true}, {text: "and the docs"}},
	}
	s.jobs[se.id] = running{jobs: []Job{{N: 3, Command: "make test", Since: at(5)}}}
	s.mu.Lock()
	got := s.state(se)
	s.mu.Unlock()
	for _, want := range []string{
		`It waits for the owner to answer: "Push it?"`,
		"No tool runs: the model thinks or writes; the last thing it did was say, 56s ago.",
		"Background job 3 runs since 55s ago: make test",
		`Delegated task 1 runs: "find the callers"`,
		"- the report of a delegated task",
		"- an internal result",
		`- "and the docs"`,
		"Its last answer failed 1m5s ago: it broke",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("state lacks %q:\n%s", want, got)
		}
	}
	se = &session{id: "s2", events: []Event{{Kind: Canceled, At: at(0)}}}
	s.mu.Lock()
	got = s.state(se)
	s.mu.Unlock()
	if !strings.Contains(got, "Its last answer was stopped 1m0s ago.") {
		t.Errorf("state of a stopped answer:\n%s", got)
	}
}

// The thread stops a job of a session that runs.
func TestThreadStopsJob(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "hi"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	var stopped int
	s.mu.Lock()
	s.setJobs("s1", []Job{{N: 2, Command: "sleep 99"}}, func(n int) bool { stopped = n; return true })
	s.mu.Unlock()
	got, err := threading{s}.Stop(ctx, "s1", 2)
	if err != nil || stopped != 2 || !strings.HasPrefix(got, "Job 2 stopped") {
		t.Fatalf("%q %v, stopped %d", got, err, stopped)
	}
}
