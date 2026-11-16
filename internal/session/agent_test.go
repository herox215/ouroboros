package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/agents"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// Every session runs as an agent of the profile: scheduled work and work a
// part of ouro starts as the agent it names, the agent of the thread where it
// names none, and a session of the owner as the agent of the thread. Its
// answers run as it, the list says it, and the archive keeps it, so that the
// session runs as it again once the node started anew.
func TestSessionsRunAsTheirAgent(t *testing.T) {
	dir := t.TempDir()
	ran := make(chan string, 8)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasPrefix(text, backgroundHead) {
			return "", nil // the thread, told what came of a job
		}
		ran <- assistant.AgentOf(ctx)
		return "done", nil
	}}
	s := service(t, a)
	s.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })

	archived, result, err := s.RunScheduled(t.Context(), "Archive memories", "tidy", "", agents.Archivist, false)
	if err != nil {
		t.Fatal(err)
	}
	scheduledResult(t, result)
	if got := <-ran; got != agents.Archivist {
		t.Fatalf("the scheduled work ran as %q", got)
	}
	plain, result, err := s.RunScheduled(t.Context(), "Morning", "the calendar", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	scheduledResult(t, result)
	if got := <-ran; got != agents.Thread {
		t.Fatalf("a job that names no agent ran as %q", got)
	}
	looked, result, err := s.RunWork(t.Context(), Work{Name: "Board orchestrator", Text: "look", Agent: agents.Orchestrator})
	if err != nil {
		t.Fatal(err)
	}
	scheduledResult(t, result)
	if got := <-ran; got != agents.Orchestrator {
		t.Fatalf("the work ran as %q", got)
	}
	owned, err := s.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{archived: agents.Archivist, plain: agents.Thread, looked: agents.Orchestrator, owned: agents.Thread}
	list, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range list {
		if info.Agent != want[info.ID] {
			t.Fatalf("the list says %s runs as %q, not %q", info.ID, info.Agent, want[info.ID])
		}
	}

	// A node that starts anew finds them in the archive, and runs them as
	// they were.
	next := New(a)
	t.Cleanup(next.Close)
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	list, err = next.List(t.Context())
	if err != nil || len(list) != len(want) {
		t.Fatalf("the archived list: %+v, %v", list, err)
	}
	for _, info := range list {
		if info.Agent != want[info.ID] {
			t.Fatalf("the archive says %s runs as %q, not %q", info.ID, info.Agent, want[info.ID])
		}
	}
	if err := next.Ask(t.Context(), archived, "once more"); err != nil {
		t.Fatal(err)
	}
	if got := <-ran; got != agents.Archivist {
		t.Fatalf("the archived session ran as %q", got)
	}
}

// Work for an agent the profile does not have does not start.
func TestWorkForAnUnknownAgent(t *testing.T) {
	a := &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		t.Error("an answer ran")
		return "", nil
	}}
	s := service(t, a)
	if _, _, err := s.RunScheduled(t.Context(), "job", "go", "", "helper", false); !errors.Is(err, agents.ErrUnknown) {
		t.Fatalf("scheduled work for no agent: %v", err)
	}
	if _, _, err := s.RunWork(t.Context(), Work{Name: "work", Text: "go", Agent: "helper"}); !errors.Is(err, agents.ErrUnknown) {
		t.Fatalf("work for no agent: %v", err)
	}
	if list, _ := s.List(t.Context()); len(list) != 0 {
		t.Fatalf("a session was kept: %+v", list)
	}
}

// The thread runs as the agent of the thread.
func TestThreadRunsAsTheAgentOfTheThread(t *testing.T) {
	ran := make(chan string, 1)
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		ran <- assistant.AgentOf(ctx)
		return "hello", nil
	}}
	s := service(t, a)
	if err := s.Ask(t.Context(), ThreadID, "hi"); err != nil {
		t.Fatal(err)
	}
	if got := <-ran; got != agents.Thread {
		t.Fatalf("the thread ran as %q", got)
	}
}
