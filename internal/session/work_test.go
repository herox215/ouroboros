package session

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// Work a part of ouro starts for itself runs with the tools it names and
// those of its own, takes what its starter tells it while it runs, and is
// apart: the thread is not told of the session, nor of what came of it.
func TestRunWorkIsApart(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var only []string
	var own []agent.Tool
	var told []openai.Message
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text != "do the ticket" {
			t.Errorf("an answer to %q", text)
			return "", nil
		}
		close(started)
		<-release
		only, own = assistant.OnlyTools(ctx), assistant.OwnTools(ctx)
		if assistant.MessengerOf(ctx) != nil {
			t.Error("the work reaches the thread")
		}
		if agent.CanAskOwner(ctx) {
			t.Error("work that may not ask can ask the owner")
		}
		told = agent.SteeredMessages(ctx)
		return "work output", nil
	}}
	s := service(t, a)
	_, thread, _ := s.Watch(t.Context(), ThreadID)
	id, result, err := s.RunWork(t.Context(), Work{Name: " Ticket 1 ", Text: "do the ticket", Tools: []string{"bash"}, Own: []agent.Tool{{Name: "ticket_read"}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !s.TellWork(id, "a comment") {
		t.Fatal("work that runs was not told")
	}
	if s.TellWork("nope", "a comment") || s.TellWork(ThreadID, "a comment") {
		t.Fatal("what is no work was told")
	}
	if state := s.WorkState(id); !strings.Contains(state, `"Ticket 1"`) || !strings.Contains(state, "An answer is on its way") {
		t.Fatalf("state: %s", state)
	}
	if s.WorkState("nope") != "" || s.WorkState(ThreadID) != "" {
		t.Fatal("what is no work has a state of work")
	}
	s.mu.Lock()
	routable := s.routable()
	s.mu.Unlock()
	if len(routable) != 0 {
		t.Fatalf("the thread is told of the work: %+v", routable)
	}
	close(release)
	if out := scheduledResult(t, result); out.Text != "work output" || out.Err != nil {
		t.Fatalf("result: %+v", out)
	}
	if !slices.Equal(only, []string{"bash"}) || len(own) != 1 || own[0].Name != "ticket_read" {
		t.Fatalf("tools %v, own %+v", only, own)
	}
	if len(told) != 1 || told[0].Content != "a comment" {
		t.Fatalf("told: %+v", told)
	}
	if s.TellWork(id, "too late") {
		t.Fatal("work that ended was told")
	}
	a.mu.Lock()
	notes := slices.Clone(a.notes)
	a.mu.Unlock()
	if len(notes) != 0 {
		t.Fatalf("the thread was noted of the work: %q", notes)
	}
	select {
	case e := <-thread:
		t.Fatalf("the thread was told %+v", e)
	default:
	}
	past, _, _ := s.Watch(t.Context(), id)
	if past[0].Kind != Title || past[0].Text != "Ticket 1" {
		t.Fatalf("the work is not named: %+v", past[0])
	}
}

// What the work is told after it took its last message goes with it: no
// answer of its own starts for that.
func TestWorkToldTooLate(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var answers atomic.Int32
	a := &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		if answers.Add(1) == 1 {
			close(started)
			<-release
		}
		return "done", nil
	}}
	s := service(t, a)
	id, result, err := s.RunWork(t.Context(), Work{Text: "do the ticket"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if !s.TellWork(id, "a comment") {
		t.Fatal("work that runs was not told")
	}
	close(release)
	scheduledResult(t, result)
	s.mu.Lock()
	se := s.sessions[id]
	queued, running, title := len(se.queue), se.cancel != nil, se.title
	s.mu.Unlock()
	if queued != 0 || running || answers.Load() != 1 {
		t.Fatalf("%d queued, running %v, %d answers", queued, running, answers.Load())
	}
	if title != "Work" {
		t.Fatalf("work without a name is called %q", title)
	}
}

// What a call of the work needs the leave of the owner for is asked through
// the thread, and the work stays apart all the same: the thread gets no
// report of how it ended.
func TestWorkAsksThroughTheThread(t *testing.T) {
	var reports []string
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasPrefix(text, backgroundHead) {
			reports = append(reports, text)
			return "asked the owner", nil
		}
		if !agent.CanAskOwner(ctx) {
			t.Error("work that may ask cannot")
		}
		return agent.AskOwner(ctx, json.RawMessage(`{"question":"Allow operation?","options":["Run it this time","Don't run it"]}`))
	}}
	s := service(t, a)
	s.Watch(t.Context(), ThreadID)
	id, result, err := s.RunWork(t.Context(), Work{Name: "Ticket", Text: "do the ticket", Ask: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.answerMessage(t.Context(), waitingMessage(t, s), "Run it this time", false); err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); out.Text != "Run it this time" || out.Err != nil {
		t.Fatalf("result: %+v", out)
	}
	s.Close() // every answer ended, also one of the thread
	s.mu.Lock()
	routed := s.sessions[id].routed
	s.mu.Unlock()
	if routed {
		t.Fatal("the work is the thread's after it was answered")
	}
	if len(reports) != 1 || !strings.Contains(reports[0], "sends message") || strings.Contains(reports[0], "completed") {
		t.Fatalf("the thread was told: %q", reports)
	}
	past, _, _ := s.Watch(context.Background(), id)
	for _, e := range past {
		if e.Kind == Message && e.Routed {
			t.Fatalf("a message of the work is the thread's: %+v", e)
		}
	}
}

// Work the node only finds in its archive is still apart for the thread,
// where who started it says so.
func TestApartInTheArchive(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	for _, id := range []string{"ticket", "topic"} {
		arch.Append(history.Who{Session: id}, []openai.Message{{Role: "user", Content: "from before"}}, nil)
		arch.SetTitle(history.Owner(id), "Kept "+id)
	}
	entries, err := arch.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries %+v, %v", entries, err)
	}
	arch.Close()
	s, _, _ := archived(t, &fakeAgent{}, dir, entries[0].Modified)
	list := func() []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		var ids []string
		for _, r := range s.routable() {
			ids = append(ids, r.id)
		}
		slices.Sort(ids)
		return ids
	}
	if got := list(); !slices.Equal(got, []string{"ticket", "topic"}) {
		t.Fatalf("without anyone to ask: %v", got)
	}
	s.Apart = func(id string) bool { return id == "ticket" }
	if got := list(); !slices.Equal(got, []string{"topic"}) {
		t.Fatalf("with work in the archive: %v", got)
	}
}

// Work is refused as a scheduled run is: without a text, and by a node that
// stopped.
func TestRunWorkRefused(t *testing.T) {
	s := service(t, &fakeAgent{})
	if _, _, err := s.RunWork(t.Context(), Work{Name: "Ticket", Text: " "}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("work without a text: %v", err)
	}
	s.Close()
	if _, _, err := s.RunWork(t.Context(), Work{Name: "Ticket", Text: "do it"}); !errors.Is(err, ErrStopped) {
		t.Fatalf("work for a node that stopped: %v", err)
	}
}
