package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// efforts is a fakeAgent that keeps the effort of every answer, read when
// the answer asks for it, and that of the node.
type efforts struct {
	mu   sync.Mutex
	took []string
}

func (e *efforts) agent(node string, gate <-chan struct{}) *fakeAgent {
	return &fakeAgent{effort: node, run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "wait" {
			<-gate
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.took = append(e.took, text+":"+openai.EffortOf(ctx))
		return "pong", nil
	}}
}

func (e *efforts) all() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.took, " ")
}

// Each session answers with its own effort, and one without with that of the
// node. An effort that is set holds from the next call on, also in the
// answer on its way, and is told to who watches.
func TestEffortPerSession(t *testing.T) {
	e := &efforts{}
	gate := make(chan struct{})
	a := e.agent("medium", gate)
	a.size, a.cache = 42, assistant.Cache{Tokens: 7}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.SetEffort(ctx, "s1", "high"); err != nil {
		t.Fatal(err)
	}
	if got := until(t, live, Effort); got[0].Text != "high" {
		t.Fatalf("told %+v", got)
	}
	asked(t, s, "s1", "one")
	asked(t, s, "s2", "two")
	s.SetEffort(ctx, "s2", "")
	asked(t, s, "s2", "auto")

	// Set while the answer is on its way, it holds for the next call.
	_, waiting, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "wait")
	s.SetEffort(ctx, "s1", "low")
	close(gate)
	until(t, waiting, Answer)
	if got := e.all(); got != "one:high two:medium auto: wait:low" {
		t.Fatalf("efforts %q", got)
	}

	// Who watches anew is told the effort last; a session without one is
	// told none. Only what was said makes a session listed.
	// It tells the context and the cache as the last Event did, so that
	// what is shown of them stays.
	if past, _, _ := s.Watch(ctx, "s1"); past[len(past)-1].Kind != Effort || past[len(past)-1].Text != "low" ||
		past[len(past)-1].Context != 42 || past[len(past)-1].Cache.Tokens != 7 {
		t.Fatalf("past %+v", past)
	}
	s.SetEffort(ctx, "quiet", "max")
	if past, _, _ := s.Watch(ctx, "s3"); len(past) != 0 {
		t.Fatalf("past %+v", past)
	}
	if list, _ := s.List(ctx); len(list) != 2 {
		t.Fatalf("list %+v", list)
	}
	// A session that only has an effort is kept, though nobody watches it.
	watch, stop := context.WithCancel(ctx)
	if past, _, _ := s.Watch(watch, "quiet"); len(past) != 1 || past[0].Text != "max" {
		t.Fatalf("past %+v", past)
	}
	stop()
	for gone := false; !gone; { // the watcher is let go
		s.mu.Lock()
		se, kept := s.sessions["quiet"]
		gone = !kept || len(se.watchers) == 0
		s.mu.Unlock()
		if !kept {
			t.Fatal("a session with an effort was forgotten")
		}
	}

	// What is no effort, or no session, is refused.
	if err := s.SetEffort(ctx, "s1", "huge"); !errors.Is(err, ErrNoEffort) || !strings.Contains(err.Error(), `"huge", there are none, minimal, low, medium, high, xhigh, max`) {
		t.Fatalf("no effort: %v", err)
	}
	if err := s.SetEffort(ctx, "../x", "low"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("no session: %v", err)
	}
	s.Close()
	if err := s.SetEffort(ctx, "s1", "low"); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped: %v", err)
	}
}

// The effort of a session is kept along with it: loaded again, also before
// anything was said in it, it has it. What cannot be written or read is
// logged.
func TestEffortIsKept(t *testing.T) {
	dir := t.TempDir()
	e := &efforts{}
	s, arch, l := archived(t, e.agent("medium", nil), dir, time.Now())
	ctx := context.Background()
	s.SetEffort(ctx, "s1", "high")
	s.SetEffort(ctx, "empty", "")
	asked(t, s, "s1", "one")
	arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "pong"}}, nil)
	s.Close()
	if effort, ok, _ := arch.Effort(history.Owner("s1")); !ok || effort != "high" || len(l.lines) != 0 {
		t.Fatalf("kept %q, %v, log %q", effort, ok, l.lines)
	}

	s, _, l = archived(t, e.agent("medium", nil), dir, time.Now())
	if past, _, _ := s.Watch(ctx, "s1"); kinds(past) != "message answer effort" || past[2].Text != "high" {
		t.Fatalf("loaded %+v", past)
	}
	if past, _, _ := s.Watch(ctx, "empty"); kinds(past) != "effort" || past[0].Text != "" {
		t.Fatalf("empty %+v", past)
	}
	asked(t, s, "s1", "again")
	asked(t, s, "empty", "first")
	// The agent is told that ouro started anew along with the first message.
	if got := e.all(); !strings.HasPrefix(got, "one:high [A note of ouro") || !strings.HasSuffix(got, "again:high first:") {
		t.Fatalf("efforts %q", got)
	}

	// What cannot be written or read is logged; the session goes on.
	os.MkdirAll(filepath.Join(dir, "owner", "broken.effort", "x"), 0o700)
	if err := s.SetEffort(ctx, "broken", "low"); err != nil {
		t.Fatal(err)
	}
	if !l.has("the effort of the session broken cannot be kept") {
		t.Fatalf("log %q", l.lines)
	}
	s.Close()
	s, _, l = archived(t, e.agent("medium", nil), dir, time.Now())
	if past, _, _ := s.Watch(ctx, "broken"); len(past) != 0 || !l.has("the effort of the session broken cannot be read") {
		t.Fatalf("past %+v, log %q", past, l.lines)
	}
}

// The effort crosses the socket, and one that is none is told apart.
func TestClientEffort(t *testing.T) {
	e := &efforts{}
	s := service(t, e.agent("", nil))
	c := NewClient(serve(t, Handler(s)).dir)
	ctx := context.Background()
	_, live, _ := c.Watch(ctx, "s1")
	if err := c.SetEffort(ctx, "s1", "max"); err != nil {
		t.Fatal(err)
	}
	if got := until(t, live, Effort); got[0].Text != "max" {
		t.Fatalf("told %+v", got)
	}
	if err := c.SetEffort(ctx, "s1", "huge"); !errors.Is(err, ErrNoEffort) {
		t.Fatalf("no effort: %v", err)
	}
}
