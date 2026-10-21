package session

import (
	"context"
	"encoding/json"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// The thread tells which session its agent started, so a front end can show
// beside the thread the sessions a part of it concerns.
func TestThreadTellsWhichSessionItStarted(t *testing.T) {
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	s := service(t, a)
	id, err := threading{s}.Start(context.Background(), "go", "")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	events := s.get(ThreadID).events
	s.mu.Unlock()
	last := events[len(events)-1]
	if last.Kind != Handed || last.Text != id {
		t.Fatalf("the thread tells %+v, not the session %s it started", last, id)
	}
}

// Read back from the archive, the thread tells again which sessions its
// agent handed messages to or started, as the finished calls tell; failed
// and other calls tell nothing.
func TestThreadTellsHandedAgainFromTheArchive(t *testing.T) {
	call := func(id, tool, args string) history.Call { return history.Call{ID: id, Name: tool, Arguments: args} }
	events, _ := replay([]history.Record{
		{Seq: 1, Answer: 1, Role: "user", Content: "go"},
		{Seq: 2, Answer: 1, Role: "assistant", ToolCalls: []history.Call{
			call("a", assistant.ToolSessionSend, `{"session":"abcd1234","text":"more"}`),
			call("b", assistant.ToolSessionNew, `{"text":"go"}`),
			call("c", assistant.ToolSessionNew, `{"text":"go"}`),
			call("d", assistant.ToolSessionState, `{"session":"abcd1234"}`),
		}},
		{Seq: 3, Answer: 1, Role: "tool", ToolCallID: "a", Content: `Handed to "A": answered.`},
		{Seq: 4, Answer: 1, Role: "tool", ToolCallID: "b", Content: "Work started in the background in session ef567890. Briefly acknowledge."},
		{Seq: 5, Answer: 1, Role: "tool", ToolCallID: "c", Content: "error: no session"},
		{Seq: 6, Answer: 1, Role: "tool", ToolCallID: "d", Content: "running"},
		{Seq: 7, Answer: 1, Role: "end", Outcome: "answered"},
	})
	var handed []string
	for _, e := range events {
		if e.Kind == Handed {
			handed = append(handed, e.Text)
		}
	}
	if len(handed) != 2 || handed[0] != "abcd1234" || handed[1] != "ef567890" {
		t.Fatalf("handed %q", handed)
	}
}

func TestHandedBy(t *testing.T) {
	step := func(tool, args, err string) Event {
		return Event{Kind: Step, Tool: tool, Args: json.RawMessage(args), Done: true, Err: err}
	}
	for _, c := range []struct {
		step   Event
		result string
		want   string
	}{
		{step(assistant.ToolSessionSend, `{"session":"s1"}`, ""), "", "s1"},
		{step(assistant.ToolSessionSend, `[1]`, ""), "", ""},
		{step(assistant.ToolSessionSend, `{"session":"s1"}`, "failed"), "", ""},
		{step(assistant.ToolSessionNew, `{}`, ""), "Work started in the background in session s2. Briefly", "s2"},
		{step(assistant.ToolSessionNew, `{}`, ""), "Work started in the background.", ""},
		{step("bash", `{}`, ""), "in session s3.", ""},
	} {
		if got := handedBy(c.step, c.result); got != c.want {
			t.Errorf("%s %s %q: %q, want %q", c.step.Tool, c.step.Args, c.result, got, c.want)
		}
	}
}
