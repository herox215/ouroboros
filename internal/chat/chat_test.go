package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// views is a plugin context with a view of the tool "read".
func views(t *testing.T) *plugin.Context {
	t.Helper()
	var ctx *plugin.Context
	if err := kernel.New().Start(plugin.New("views", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		plugin.Add(c, plugin.ToolView{Tool: "read", Show: func(json.RawMessage) string { return "reading a file" },
			Detail: func(s plugin.Step) string { return "✓ " + s.Remark }})
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestShow(t *testing.T) {
	ctx := views(t)
	at := time.Unix(1, 0)
	read := plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "read", CallID: "c1", At: at}
	if got := Show(ctx, read); got.Text != "reading a file" || got.Tool != "read" || got.CallID != "c1" || got.Done || !got.At.Equal(at) {
		t.Fatalf("a call: %+v", got)
	}
	read.Done, read.Remark, read.Err = true, "3 lines", "gone"
	if got := Show(ctx, read); got.Text != "reading a file ✓ 3 lines" || !got.Failed {
		t.Fatalf("a call that ended: %+v", got)
	}
	// A call without a view shows as nothing, and so does every call
	// without the plugins to ask.
	if got := Show(ctx, plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "echo", Done: true}); got.Text != "" || got.Tool != "echo" {
		t.Fatalf("a call without a view: %+v", got)
	}
	if got := Show(nil, read); got.Text != "" || got.Tool != "read" {
		t.Fatalf("without plugins: %+v", got)
	}
	task := plugin.SessionEvent{Kind: plugin.SessionTask, N: 2, Remark: "find the callers", Took: 2500 * time.Millisecond}
	if got := Show(ctx, task); got.Text != "task 2 · find the callers ✓ 3s" || got.Failed {
		t.Fatalf("a task: %+v", got)
	}
	task.Err = "no\n  callers"
	if got := Show(ctx, task); got.Text != "task 2 · find the callers ✗ no callers" || !got.Failed {
		t.Fatalf("a failed task: %+v", got)
	}
	for n, want := range map[int]string{3277: "3.2 KiB", 1024: "1.0 KiB", 1023: "1023 bytes"} {
		if got := Show(ctx, plugin.SessionEvent{Kind: plugin.SessionCalling, N: n}); got.Text != want {
			t.Fatalf("a call of %d bytes: %q", n, got.Text)
		}
	}
	if got := Show(ctx, plugin.SessionEvent{Kind: plugin.SessionWorking, N: 3, Own: true}); got.N != 3 || !got.Own {
		t.Fatalf("working: %+v", got)
	}
	asked := plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Which database?","options":["SQLite","Postgres"],"recommended":"SQLite"}`)}
	if got := Show(ctx, asked); got.Text != "Which database?" || len(got.Options) != 2 || got.Recommended != "SQLite" {
		t.Fatalf("a question: %+v", got)
	}
	asked.Done, asked.Text = true, "Postgres"
	if got := Show(ctx, asked); got.Text != "Postgres" || got.Options != nil {
		t.Fatalf("an answered question: %+v", got)
	}
	message := plugin.SessionEvent{Kind: plugin.SessionMessage, Text: "hello", Routed: true, Context: 42}
	if got := Show(ctx, message); got.Text != "hello" || !got.Routed || got.Context != 42 {
		t.Fatalf("a message: %+v", got)
	}
}

// What is on its way, as the events of a session tell it.
func TestStateApply(t *testing.T) {
	var s State
	s.Apply(Event{Kind: plugin.SessionMessage, Text: "go"})
	if !s.Pending || !s.Running {
		t.Fatalf("a message: %+v", s)
	}
	s.Apply(Event{Kind: plugin.SessionQueued, Text: "one"})
	s.Apply(Event{Kind: plugin.SessionQueued, Text: "two"})
	s.Apply(Event{Kind: plugin.SessionQueued, Text: "three"})
	s.Apply(Event{Kind: plugin.SessionUnqueued})
	if fmt.Sprint(s.Queued) != "[one two]" {
		t.Fatalf("queue: %q", s.Queued)
	}
	// The first that was queued is the next message.
	s.Apply(Event{Kind: plugin.SessionMessage, Text: "one"})
	if fmt.Sprint(s.Queued) != "[two]" {
		t.Fatalf("taken: %q", s.Queued)
	}
	s.Apply(Event{Kind: plugin.SessionAnswer})
	if s.Pending || s.Running {
		t.Fatalf("answered: %+v", s)
	}
	s.Apply(Event{Kind: plugin.SessionUnqueued})
	s.Apply(Event{Kind: plugin.SessionUnqueued})
	s.Apply(Event{Kind: plugin.SessionMessage})
	if len(s.Queued) != 0 {
		t.Fatalf("an empty queue: %q", s.Queued)
	}
	for _, end := range []plugin.SessionKind{plugin.SessionFailed, plugin.SessionCanceled} {
		s.Apply(Event{Kind: plugin.SessionTask})
		if !s.Pending || !s.Running {
			t.Fatalf("a task: %+v", s)
		}
		s.Apply(Event{Kind: end})
		if s.Pending || s.Running {
			t.Fatalf("%s: %+v", end, s)
		}
	}
	// What works for the thread beside it does not keep it pending.
	s.Apply(Event{Kind: plugin.SessionMessage})
	s.Apply(Event{Kind: plugin.SessionWorking, N: 3, Own: true})
	if !s.Pending || s.Active() != 2 {
		t.Fatalf("working: %+v, %d active", s, s.Active())
	}
	s.Apply(Event{Kind: plugin.SessionAnswer})
	if s.Pending || s.Active() != 2 {
		t.Fatalf("the thread answered: %+v", s)
	}
	s.Apply(Event{Kind: plugin.SessionWorking, N: 0})
	if s.Active() != 0 {
		t.Fatalf("idle: %+v", s)
	}
	s.Apply(Event{Kind: plugin.SessionWorking, N: 0, Own: true})
	if s.Active() != 0 {
		t.Fatalf("a count below none: %d", s.Active())
	}
	s.Apply(Event{Kind: plugin.SessionMessage})
	s.Apply(Event{Kind: plugin.SessionWorking, N: 0})
	if s.Pending || s.Running {
		t.Fatalf("nothing works: %+v", s)
	}
}

// A question waits until the wait for its answer is over: not the answer
// that asked it, nor a message ends it, as in the thread the answer of one
// session comes while another asks.
func TestQuestion(t *testing.T) {
	var s State
	s.Apply(Event{Kind: plugin.SessionMessage})
	s.Apply(Event{Kind: plugin.SessionAsked, Text: "Which?", Options: []string{"a", "b"}, Recommended: "b"})
	if s.Asking == nil || s.Asking.Text != "Which?" || s.Asking.Recommended != "b" || len(s.Asking.Options) != 2 {
		t.Fatalf("asked: %+v", s.Asking)
	}
	for _, kind := range []plugin.SessionKind{plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled, plugin.SessionMessage} {
		s.Apply(Event{Kind: kind})
		if s.Asking == nil {
			t.Fatalf("%s ended the question", kind)
		}
	}
	s.Apply(Event{Kind: plugin.SessionAsked, Text: "Which other?"})
	if s.Asking.Text != "Which other?" {
		t.Fatalf("another question: %+v", s.Asking)
	}
	s.Apply(Event{Kind: plugin.SessionAsked, Done: true, Text: "a"})
	if s.Asking != nil {
		t.Fatalf("answered: %+v", s.Asking)
	}
}

// What the owner writes answers the question, joins the answer on its way or
// starts one; the node not taking it gives it back.
func TestSend(t *testing.T) {
	var s State
	if way, err := s.Send(); err != nil || way != Ask || !s.Pending {
		t.Fatalf("idle: %v %v %+v", way, err, s)
	}
	if way, err := s.Send(); err != nil || way != Steer {
		t.Fatalf("on its way: %v %v", way, err)
	}
	s.Unsent()
	if s.Pending {
		t.Fatal("pending without an answer the node took")
	}
	s.Apply(Event{Kind: plugin.SessionMessage})
	s.Apply(Event{Kind: plugin.SessionAsked, Text: "Which?"})
	if way, err := s.Send(); err != nil || way != Answer || !s.Asking.Sent {
		t.Fatalf("asked: %v %v %+v", way, err, s.Asking)
	}
	// Once the answer is on its way, what follows joins the answer.
	if way, err := s.Send(); err != nil || way != Steer {
		t.Fatalf("answered: %v %v", way, err)
	}
	s.Unsent()
	if s.Asking.Sent || !s.Pending {
		t.Fatalf("not taken: %+v", s)
	}
	s.Lost()
	if s.Pending || s.Running || s.Asking != nil {
		t.Fatalf("lost: %+v", s)
	}
	s.Unsent()
	peer := State{Peer: "beta"}
	if _, err := peer.Send(); !errors.Is(err, ErrReadOnly) || peer.Pending {
		t.Fatalf("a peer: %v %+v", err, peer)
	}
}

// The last message queued is taken back into an empty input, before what
// is written meanwhile.
func TestTakeBack(t *testing.T) {
	var s State
	if s.TakesBack("") {
		t.Fatal("took back from an empty queue")
	}
	s.Apply(Event{Kind: plugin.SessionQueued, Text: "later"})
	if !s.TakesBack("") || s.TakesBack("typed") {
		t.Fatalf("queue %q", s.Queued)
	}
	for _, tc := range []struct{ text, written, want string }{
		{"later", "", "later"}, {"later", "now", "later\nnow"}, {"", "now", "now"},
	} {
		if got := TakenBack(tc.text, tc.written); got != tc.want {
			t.Errorf("TakenBack(%q, %q) = %q", tc.text, tc.written, got)
		}
	}
}

type sessions struct{ calls []string }

func (s *sessions) Ask(ctx context.Context, id, text string) error {
	return s.call(ctx, "ask", id, text)
}
func (s *sessions) Steer(ctx context.Context, id, text string) error {
	return s.call(ctx, "steer", id, text)
}
func (s *sessions) call(ctx context.Context, how, id, text string) error {
	from := openai.MessageSourceFrom(ctx).Interface
	s.calls = append(s.calls, how+":"+id+":"+text+":"+from)
	return nil
}

func TestDeliver(t *testing.T) {
	s := &sessions{}
	for _, way := range []Way{Ask, Steer, Answer} {
		if err := Deliver(context.Background(), s, "s1", "hi", way, "tui"); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(s.calls) != "[ask:s1:hi:tui steer:s1:hi:tui steer:s1:hi:tui]" {
		t.Fatalf("calls %q", s.calls)
	}
}
