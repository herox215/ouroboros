package session

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// Sources belong to individual inputs, even when two interfaces share a
// session. Queuing and taking back an input must not change its source.
func TestSourceAcrossSocket(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "first" {
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return text, nil
	}}
	s := service(t, a)
	c := NewClient(serve(t, Handler(s)).dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gui := WithMessageSource(ctx, MessageSource{Interface: "gui"})
	tui := WithMessageSource(ctx, MessageSource{Interface: "tui"})
	_, live, err := c.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ask(gui, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	got := until(t, live, Message)
	for _, input := range []struct {
		ctx  context.Context
		text string
	}{{tui, "second"}, {gui, "third"}, {tui, "withdrawn"}} {
		if err := c.Steer(input.ctx, "s1", input.text); err != nil {
			t.Fatal(err)
		}
	}
	// The caller taking it back is not necessarily the one who sent it.
	if text, err := c.Unsteer(gui, "s1"); err != nil || text != "withdrawn" {
		t.Fatalf("unsteer: %q, %v", text, err)
	}
	got = append(got, until(t, live, Unqueued)...)
	close(gate)
	got = append(got, until(t, live, Answer)...)
	got = append(got, until(t, live, Answer)...)
	if kinds(got) != "message queued queued queued unqueued answer message message answer" {
		t.Fatalf("events: %+v", got)
	}
	for i, want := range map[int]struct{ text, source string }{
		0: {"first", "gui"}, 1: {"second", "tui"}, 2: {"third", "gui"},
		3: {"withdrawn", "tui"}, 4: {"withdrawn", "tui"},
		6: {"second", "tui"}, 7: {"third", "gui"},
	} {
		if got[i].Text != want.text || got[i].Source.Interface != want.source {
			t.Errorf("event %d: %+v, want %q from %s", i, got[i], want.text, want.source)
		}
	}
	if got[8].Text != "second\n\nthird" {
		t.Fatalf("queued answer: %+v", got[8])
	}
	// A later Ask on the same session must not inherit the first interface.
	if err := c.Ask(tui, "s1", "fourth"); err != nil {
		t.Fatal(err)
	}
	last := until(t, live, Answer)
	if kinds(last) != "message answer" || last[0].Source.Interface != "tui" {
		t.Fatalf("later ask: %+v", last)
	}
	if text, err := c.Unsteer(ctx, "s1"); err != nil || text != "" {
		t.Fatalf("queue after consumption: %q, %v", text, err)
	}
}

// Replay preserves stored metadata in agent memory, while legacy or invalid
// owner sources are shown as unknown to front ends. Neither path adds source
// context to the owner's text.
func TestSourceReplayRoundTrip(t *testing.T) {
	var records []history.Record
	if err := json.Unmarshal([]byte(`[
		{"role":"user","content":"from gui","source":{"interface":"gui"}},
		{"role":"user","content":"from gateway","source":{"interface":"api","gateway":"matrix"}},
		{"role":"user","content":"legacy"},
		{"role":"user","content":"unrecognized","source":{"interface":"future"}},
		{"role":"user","content":"explicit unknown","source":{"interface":"unknown"}},
		{"role":"assistant","content":"done"},
		{"role":"end","outcome":"answered"}
	]`), &records); err != nil {
		t.Fatal(err)
	}
	events, messages := replay(records)
	if kinds(events) != "message message message message message answer" {
		t.Fatalf("replay: %+v", events)
	}
	for i, want := range []string{"gui", "api", "unknown", "unknown", "unknown"} {
		if events[i].Source.Interface != want || events[i].Text != records[i].Content {
			t.Errorf("event %d: %+v, want source %s and original text", i, events[i], want)
		}
		if messages[i].Source != records[i].Source || messages[i].Content != records[i].Content {
			t.Errorf("memory %d: %+v, want stored source and original text", i, messages[i])
		}
	}
	if events[5].Source != (MessageSource{}) || messages[5].Source != (MessageSource{}) {
		t.Fatal("assistant answer attributed to an owner interface")
	}
	if events[1].Source != records[1].Source {
		t.Fatalf("gateway lost during replay: %+v", events[1])
	}
	dir := t.TempDir()
	if err := history.Open(dir).Append(history.Who{Session: "s1"}, messages, nil); err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{}
	s, _, _ := archived(t, a, dir, time.Now())
	past, _, err := s.Watch(t.Context(), "s1")
	if err != nil || len(past) != len(events) {
		t.Fatalf("reopened: %+v, %v", past, err)
	}
	for i := range past {
		if past[i].Kind != events[i].Kind || past[i].Text != events[i].Text || past[i].Source != events[i].Source {
			t.Errorf("reopened event %d: %+v, want %+v", i, past[i], events[i])
		}
	}
	var resumed []openai.Message
	if err := json.Unmarshal(a.resumed["s1"], &resumed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed, messages) {
		t.Fatalf("resumed memory: %+v, want %+v", resumed, messages)
	}
}
