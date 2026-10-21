package session

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
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
	// Looking at a session or stopping it concerns the part on the way too.
	for _, look := range []func() error{
		func() error { _, err := threading{s}.State(context.Background(), id); return err },
		func() error { _, err := threading{s}.Stop(context.Background(), id, 0); return err },
	} {
		if err := look(); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		events := s.get(ThreadID).events
		s.mu.Unlock()
		if last := events[len(events)-1]; last.Kind != Handed || last.Text != id {
			t.Fatalf("the thread tells %+v, not the session %s it looked at", last, id)
		}
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
		{Seq: 8, Answer: 2, Role: "user", Content: backgroundHead + `"Lorem (Test)" (77aa88bb) completed. This is internal result data.]\n\nfine`},
		{Seq: 9, Answer: 2, Role: "user", Content: backgroundHead + `"Odd" without a handle]`},
		{Seq: 10, Answer: 2, Role: "user", Content: backgroundHead + `"Build (wrong) completed. details" (abcd1234) completed. This is internal result data.]`},
		{Seq: 11, Answer: 2, Role: "user", Content: backgroundHead + `"Unquoted without an end`},
		{Seq: 12, Answer: 2, Role: "user", Content: backgroundHead + `"Open" (dangling`},
	})
	var handed []string
	for _, e := range events {
		if e.Kind == Handed {
			handed = append(handed, e.Text)
		}
	}
	if strings.Join(handed, " ") != "abcd1234 ef567890 abcd1234 77aa88bb abcd1234" {
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
		{step(assistant.ToolSessionState, `{"session":"s4"}`, ""), "", "s4"},
		{step(assistant.ToolSessionStop, `{"session":"s5","job":1}`, ""), "", "s5"},
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

// A background result concerns the part of the thread that takes it: told
// as it is taken, after the owner's message taken before it, the same order
// as the archive keeps them, see replay.
func TestThreadTellsReportedAsItTakesIt(t *testing.T) {
	s := service(t, &fakeAgent{})
	report := backgroundHead + `"Build (wrong) completed. x" (abcd1234) completed. This is internal result data.]`
	s.mu.Lock()
	thread := s.get(ThreadID)
	s.taken(thread, []queued{{text: "next"}, {generated: true, text: report}})
	events := slices.Clone(thread.events)
	s.mu.Unlock()
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, string(e.Kind)+":"+e.Text)
	}
	if strings.Join(kinds, " ") != "message:next handed:abcd1234" {
		t.Fatalf("live %q", kinds)
	}
	replayed, _ := replay([]history.Record{{Seq: 1, Answer: 1, Role: "user", Content: "next"}, {Seq: 2, Answer: 1, Role: "user", Content: report}})
	kinds = kinds[:0]
	for _, e := range replayed {
		kinds = append(kinds, string(e.Kind)+":"+e.Text)
	}
	if strings.Join(kinds, " ") != "message:next handed:abcd1234" {
		t.Fatalf("replayed %q", kinds)
	}
}
