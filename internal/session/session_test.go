package session

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

// fakeAgent answers as its run says, "pong" without one.
type fakeAgent struct {
	mu       sync.Mutex
	ready    error
	run      func(ctx context.Context, text string, observe func(assistant.Step), say func(string)) (string, error)
	size     int
	contents assistant.Contents
	failed   error // of Contents
	inspect  *agent.Inspection
	resumed  map[string]json.RawMessage
	resume   error
}

func (a *fakeAgent) Ready() error { return a.ready }
func (a *fakeAgent) Run(ctx context.Context, _, text string, observe func(assistant.Step), say func(string)) (string, error) {
	if a.run != nil {
		return a.run(ctx, text, observe, say)
	}
	return "pong", nil
}
func (a *fakeAgent) Context(string) int                          { return a.size }
func (a *fakeAgent) Contents(string) (assistant.Contents, error) { return a.contents, a.failed }
func (a *fakeAgent) Inspection(string) (agent.Inspection, bool) {
	if a.inspect == nil {
		return agent.Inspection{}, false
	}
	return *a.inspect, true
}
func (a *fakeAgent) Conversation(id string) json.RawMessage {
	return json.RawMessage(`[{"role":"user","content":"of ` + id + `"}]`)
}
func (a *fakeAgent) Resume(id string, memory json.RawMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resumed == nil {
		a.resumed = map[string]json.RawMessage{}
	}
	a.resumed[id] = memory
	return a.resume
}

// service is a Service with a, closed when the test ends.
func service(t *testing.T, a *fakeAgent) *Service {
	t.Helper()
	s := New(a)
	t.Cleanup(s.Close)
	return s
}

// until reads live until an Event of one of kinds came, and returns what
// came on the way.
func until(t *testing.T, live <-chan Event, kinds ...Kind) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-live:
			if !ok {
				t.Fatalf("the events ended after %+v", got)
			}
			got = append(got, e)
			if slices.Contains(kinds, e.Kind) {
				return got
			}
		case <-timeout:
			t.Fatalf("no %v after %+v", kinds, got)
		}
	}
}

// kinds are the kinds of events.
func kinds(events []Event) string {
	var k []string
	for _, e := range events {
		k = append(k, string(e.Kind))
	}
	return strings.Join(k, " ")
}

// What happens on the way to an answer is told to who watches the session,
// in order: the message, what the agent says, the calls of tools, and the
// answer, each with the size of the context.
func TestAnswerIsTold(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{size: 42, run: func(_ context.Context, text string, observe func(assistant.Step), say func(string)) (string, error) {
		say("I will look")
		observe(assistant.Step{Tool: "read", Args: json.RawMessage(`{"path":"~/a"}`)})
		observe(assistant.Step{Tool: "read", Args: json.RawMessage(`{"path":"~/a"}`), Done: true, Took: time.Second, Err: errors.New("no such file")})
		<-gate
		return "it is not there: " + text, nil
	}}
	s := service(t, a)
	ctx := context.Background()
	past, live, err := s.Watch(ctx, "s1")
	if err != nil || len(past) != 0 {
		t.Fatalf("past %+v, %v", past, err)
	}
	if err := s.Ask(ctx, "s1", "where is a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(ctx, "s1", "and b?"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second message while the first is answered: %v", err)
	}
	got := until(t, live, Step, Step)
	got = append(got, until(t, live, Step)...)
	if kinds(got) != "message say step step" || got[0].Text != "where is a" || got[1].Text != "I will look" || got[0].Context != 42 {
		t.Fatalf("events %+v", got)
	}
	if st := got[3].AsStep(); st.Tool != "read" || !st.Done || st.Took != time.Second || st.Err.Error() != "no such file" || string(st.Args) != `{"path":"~/a"}` {
		t.Fatalf("step %+v", st)
	}
	if st := got[2].AsStep(); st.Err != nil || st.Done {
		t.Fatalf("step %+v", st)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0] != (Info{ID: "s1", Title: "where is a", Running: true, Last: got[3].At}) {
		t.Fatalf("list %+v", list)
	}
	close(gate)
	if end := until(t, live, Answer); end[0].Text != "it is not there: where is a" {
		t.Fatalf("answer %+v", end)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Running {
		t.Fatalf("list %+v", list)
	}
	// Whoever watches later gets all of it.
	past, _, _ = s.Watch(ctx, "s1")
	if kinds(past) != "message say step step answer" {
		t.Fatalf("past %+v", past)
	}
}

// An answer runs with a context of the Service: it goes on when nobody
// watches any more, and two who watch see the same.
func TestAnswerGoesOnWithoutTheClient(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		select {
		case <-gate:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	s := service(t, a)
	ctx, leave := context.WithCancel(context.Background())
	_, first, _ := s.Watch(ctx, "s1")
	_, second, _ := s.Watch(context.Background(), "s1")
	s.Ask(ctx, "s1", "hi")
	until(t, first, Message)
	leave()
	for range first { // let go
	}
	close(gate)
	if got := until(t, second, Answer); kinds(got) != "message answer" || got[1].Text != "done" {
		t.Fatalf("events %+v", got)
	}
}

// An answer that fails says why; one that is stopped says so.
func TestFailedAndCanceled(t *testing.T) {
	started := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "fail" {
			return "", errors.New("the LLM is down")
		}
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "fail")
	if got := until(t, live, Failed); got[len(got)-1].Text != "the LLM is down" {
		t.Fatalf("events %+v", got)
	}
	if err := s.Cancel(ctx, "s1"); err != nil {
		t.Fatalf("nothing to stop: %v", err)
	}
	if err := s.Cancel(ctx, "nobody"); err != nil {
		t.Fatalf("no such session: %v", err)
	}
	s.Ask(ctx, "s1", "wait")
	<-started
	if err := s.Cancel(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if got := until(t, live, Canceled); got[len(got)-1].Text != "" {
		t.Fatalf("events %+v", got)
	}
}

// What names no session, or holds no text, is refused, and so is a message
// the agent cannot answer.
func TestRefused(t *testing.T) {
	a := &fakeAgent{}
	s := service(t, a)
	ctx := context.Background()
	long := strings.Repeat("x", maxID+1)
	for _, id := range []string{"", long} {
		if err := s.Ask(ctx, id, "hi"); !errors.Is(err, ErrNoSession) {
			t.Errorf("ask %q: %v", id, err)
		}
		if _, _, err := s.Watch(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Errorf("watch %q: %v", id, err)
		}
		if err := s.Cancel(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Errorf("cancel %q: %v", id, err)
		}
		if _, err := s.Contents(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Errorf("contents %q: %v", id, err)
		}
	}
	if _, _, err := s.Watch(ctx, strings.Repeat("x", maxID)); err != nil {
		t.Errorf("the longest ID: %v", err)
	}
	if err := s.Ask(ctx, "s1", " \n "); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty: %v", err)
	}
	a.ready = errors.New("not set up")
	if err := s.Ask(ctx, "s1", "hi"); err != a.ready {
		t.Errorf("not ready: %v", err)
	}
	if list, _ := s.List(ctx); len(list) != 0 {
		t.Errorf("nothing was asked: %+v", list)
	}
	if id, err := s.New(ctx); err != nil || len(id) != 36 {
		t.Errorf("new: %q, %v", id, err)
	}
	if started, err := s.Open(ctx); started || err != nil {
		t.Errorf("open: %v, %v", started, err)
	}
}

// A watcher that falls too far behind is let go; who watches again gets
// everything.
func TestSlowWatcherIsLetGo(t *testing.T) {
	a := &fakeAgent{run: func(_ context.Context, _ string, observe func(assistant.Step), _ func(string)) (string, error) {
		for range watchBuffer + 1 {
			observe(assistant.Step{Tool: "read"})
		}
		return "done", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, slow, _ := s.Watch(ctx, "s1")
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "read a lot")
	go func() {
		for range live {
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if list, _ := s.List(ctx); !list[0].Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer should end")
		}
	}
	n := 0
	for range slow {
		n++
	}
	if n != watchBuffer {
		t.Fatalf("the slow watcher got %d events", n)
	}
	if past, _, _ := s.Watch(ctx, "s1"); len(past) != watchBuffer+3 {
		t.Fatalf("past %d", len(past))
	}
}

// The list has the sessions with a message, the latest first, each with the
// start of its first message; a session nobody wrote to and nobody watches
// is gone.
func TestList(t *testing.T) {
	s := service(t, &fakeAgent{})
	clock := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	ctx := context.Background()
	title := strings.Repeat("ä", maxTitle) // two bytes each: cut within one
	for _, id := range []string{"b", "a", "c"} {
		_, live, _ := s.Watch(ctx, id)
		s.Ask(ctx, id, title)
		until(t, live, Answer)
	}
	// The same time for b and c, told apart by their IDs.
	s.mu.Lock()
	b := s.sessions["b"]
	b.events[len(b.events)-1].At = clock
	s.mu.Unlock()
	watched, stop := context.WithCancel(ctx)
	s.Watch(watched, "empty")
	list, _ := s.List(ctx)
	var ids []string
	for _, info := range list {
		ids = append(ids, info.ID)
	}
	if strings.Join(ids, " ") != "b c a" || list[0].Title != strings.Repeat("ä", maxTitle/2) {
		t.Fatalf("list %+v", list)
	}
	// A title of the length that fits is whole; a session nobody watches
	// any more stays with what is in it.
	exact := strings.Repeat("x", maxTitle)
	gone, leave := context.WithCancel(ctx)
	_, live, _ := s.Watch(gone, "d")
	s.Ask(ctx, "d", exact)
	until(t, live, Answer)
	leave()
	for range live {
	}
	if list, _ := s.List(ctx); len(list) != 4 || list[0].ID != "d" || list[0].Title != exact {
		t.Fatalf("list %+v", list)
	}
	stop()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		s.mu.Lock()
		_, left := s.sessions["empty"]
		s.mu.Unlock()
		if !left {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an empty session nobody watches should be gone")
		}
	}
}

// Released, the node stops once no answer is on its way; a message in
// between keeps it running.
func TestRelease(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		<-gate
		return "done", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	if s.Releases() {
		t.Fatal("a Service without a node stops nothing")
	}
	stops := make(chan struct{}, 8)
	s.Attach(func() { stops <- struct{}{} }, nil)
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "hi")
	if s.Releases() || len(stops) != 0 {
		t.Fatal("an answer keeps the node running")
	}
	close(gate)
	until(t, live, Answer)
	awaitStop(t, stops)
	s.Release(ctx)
	awaitStop(t, stops)
	// A message after the release keeps the node.
	s.mu.Lock()
	s.release = true
	s.mu.Unlock()
	s.Ask(ctx, "s1", "more")
	until(t, live, Answer)
	s.Ask(ctx, "s1", "again") // settles with the answer before
	until(t, live, Answer)
	if len(stops) != 0 {
		t.Fatal("a message after the release keeps the node running")
	}
	// So does a front end that watches after it.
	gate = make(chan struct{})
	s.Ask(ctx, "s1", "wait")
	s.Release(ctx)
	s.Watch(ctx, "s2")
	close(gate)
	until(t, live, Answer)
	s.Ask(ctx, "s1", "again")
	until(t, live, Answer)
	if len(stops) != 0 {
		t.Fatal("a front end that watches after the release keeps the node running")
	}
}

// awaitStop waits until stopped tells of a stop.
func awaitStop(t *testing.T, stopped <-chan struct{}) {
	t.Helper()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the node should stop")
	}
}

// A tool that asks for a new build makes the node take its own place once
// no answer is on its way; until then it takes no message. Without a node
// that can, nobody starts anew.
func TestRestart(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "rebuild" {
			return "built", nil
		}
		if text == "build" {
			return "", errors.New(map[bool]string{true: "asked", false: "nobody"}[AskRestart(ctx, "/new/ouro")])
		}
		<-gate
		return "slow", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "build")
	if got := until(t, live, Failed); got[len(got)-1].Text != "nobody" {
		t.Fatalf("without a node that starts anew: %+v", got)
	}

	replaced := make(chan string, 8)
	s.Attach(nil, func(exe string) { replaced <- exe })
	_, other, _ := s.Watch(ctx, "s2")
	s.Ask(ctx, "s2", "wait") // keeps the node until it is given
	until(t, other, Message)
	s.Ask(ctx, "s1", "build")
	got := until(t, live, Restart)
	if kinds(got) != "message failed restart" || got[1].Text != "asked" || got[2].Text != "/new/ouro" {
		t.Fatalf("events %+v", got)
	}
	if len(replaced) != 0 {
		t.Fatal("the answer on its way keeps the node")
	}
	if err := s.Ask(ctx, "s1", "more"); !errors.Is(err, ErrRestarting) {
		t.Fatalf("a message while ouro starts anew: %v", err)
	}
	if s.Releases() {
		t.Fatal("a node that starts anew is not stopped")
	}
	close(gate)
	until(t, other, Answer)
	select {
	case exe := <-replaced:
		if exe != "/new/ouro" {
			t.Fatalf("replaced by %q", exe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the node should be replaced")
	}
}

// Closed, the Service stops the answers, lets the watchers go and takes
// nothing more.
func TestClose(t *testing.T) {
	started := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	s := New(a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "wait")
	<-started
	s.Close()
	for range live {
	}
	if past, _ := s.List(ctx); len(past) != 1 || past[0].Running {
		t.Fatalf("list %+v", past)
	}
	if err := s.Ask(ctx, "s1", "more"); !errors.Is(err, ErrStopped) {
		t.Fatalf("ask: %v", err)
	}
	if _, _, err := s.Watch(ctx, "s1"); !errors.Is(err, ErrStopped) {
		t.Fatalf("watch: %v", err)
	}
	s.mu.Lock()
	last := s.sessions["s1"].events
	s.mu.Unlock()
	if last[len(last)-1].Kind != Canceled {
		t.Fatalf("events %+v", last)
	}
}

// The sessions and what the agent remembers of them go over to the node
// that takes the place of this one.
func TestHandOver(t *testing.T) {
	a := &fakeAgent{}
	s := service(t, a)
	ctx := context.Background()
	for _, id := range []string{"s2", "s1"} {
		_, live, _ := s.Watch(ctx, id)
		s.Ask(ctx, id, "hi "+id)
		until(t, live, Answer)
	}
	s.Watch(ctx, "empty") // nothing to hand over
	data := s.HandOver()

	b := &fakeAgent{}
	next := service(t, b)
	if err := next.Resume(data); err != nil {
		t.Fatal(err)
	}
	before, _ := s.List(ctx)
	after, _ := next.List(ctx)
	if !slices.Equal(before, after) || len(after) != 2 {
		t.Fatalf("before %+v, after %+v", before, after)
	}
	if string(b.resumed["s1"]) != `[{"role":"user","content":"of s1"}]` || len(b.resumed) != 2 {
		t.Fatalf("resumed %s", b.resumed)
	}
	past, _, _ := next.Watch(ctx, "s2")
	if kinds(past) != "message answer" || past[1].Text != "pong" {
		t.Fatalf("past %+v", past)
	}

	// What cannot be taken over is said so.
	if err := next.Resume([]byte("{")); err == nil {
		t.Error("no JSON")
	}
	if err := next.Resume([]byte(`[{"id":""}]`)); !errors.Is(err, ErrNoSession) {
		t.Errorf("no ID: %v", err)
	}
	b.resume = errors.New("no memory")
	if err := next.Resume(data); err == nil || !strings.Contains(err.Error(), "session s1: no memory") {
		t.Errorf("a memory that cannot be resumed: %v", err)
	}
}

// The context of a session is what the agent would send the LLM, message by
// message, with what the plugins reported.
func TestContents(t *testing.T) {
	var tool openai.Tool
	tool.Function.Name, tool.Function.Description, tool.Function.Parameters = "run", "Run JavaScript", json.RawMessage(`{"type":"object"}`)
	call := openai.ToolCall{}
	call.Function.Name, call.Function.Arguments = "run", `{"script":"1"}`
	a := &fakeAgent{contents: assistant.Contents{
		System: "You are alpha.",
		Tools:  []openai.Tool{tool},
		Messages: []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "let me see", ToolCalls: []openai.ToolCall{call}},
			{Role: "assistant", ToolCalls: []openai.ToolCall{call}}},
	}, inspect: &agent.Inspection{Diagnostics: []agent.Diagnostic{{Plugin: "post", Phase: "after", Err: errors.New("storage failed")}}}}
	s := service(t, a)
	got, err := s.Contents(context.Background(), "s1")
	want := []Line{{"system", "You are alpha."}, {"tool run", "Run JavaScript\nparameters: {\"type\":\"object\"}"}, {"user", "hi"},
		{"assistant", "let me see\n→ run {\"script\":\"1\"}"}, {"assistant", "→ run {\"script\":\"1\"}"}, {"plugin diagnostic", "post after failed: storage failed"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
	a.contents, a.inspect, a.failed = assistant.Contents{Messages: []openai.Message{{Role: "user", Content: "hi"}}}, nil, errors.New("not set up")
	if got, err := s.Contents(context.Background(), "s1"); err != a.failed || !slices.Equal(got, []Line{{"user", "hi"}}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A tool asks for a new build only of a node that can take its own place.
func TestAskRestart(t *testing.T) {
	if AskRestart(context.Background(), "/new/ouro") {
		t.Fatal("nobody starts anew here")
	}
	var asked string
	if !AskRestart(WithRestart(context.Background(), func(exe string) { asked = exe }), "/new/ouro") || asked != "/new/ouro" {
		t.Fatalf("asked %q", asked)
	}
}

// stub is an API that does as it is told.
type stub struct {
	API
	watch  error
	ask    error
	cancel error
	live   chan Event
}

func (s *stub) Watch(context.Context, string) ([]Event, <-chan Event, error) {
	return nil, s.live, s.watch
}
func (s *stub) Ask(context.Context, string, string) error { return s.ask }
func (s *stub) Cancel(context.Context, string) error      { return s.cancel }

// Await asks and waits for the answer, or for why there is none.
func TestAwait(t *testing.T) {
	started := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		switch text {
		case "fail":
			return "", errors.New("the LLM is down")
		case "wait":
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "pong", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	if got, err := Await(ctx, s, "s1", "ping"); got != "pong" || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := Await(ctx, s, "s1", "fail"); err == nil || err.Error() != "the LLM is down" {
		t.Fatalf("failed: %v", err)
	}
	stopped, stop := context.WithCancel(ctx)
	go func() {
		<-started
		stop()
	}()
	if _, err := Await(stopped, s, "s1", "wait"); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped: %v", err)
	}
	if _, err := Await(ctx, s, "", "ping"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("no session: %v", err)
	}
	if _, err := Await(ctx, s, "s1", ""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("no text: %v", err)
	}

	// An answer canceled by another client; one that cannot be stopped;
	// a node that is gone.
	live := make(chan Event, 1)
	live <- Event{Kind: Canceled}
	if _, err := Await(ctx, &stub{live: live}, "s1", "hi"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled elsewhere: %v", err)
	}
	gone, end := context.WithCancel(ctx)
	end()
	boom := errors.New("boom")
	if _, err := Await(gone, &stub{live: make(chan Event), cancel: boom}, "s1", "hi"); err != boom {
		t.Fatalf("cancel: %v", err)
	}
	close(live)
	if _, err := Await(ctx, &stub{live: live}, "s1", "hi"); err != errLost {
		t.Fatalf("lost: %v", err)
	}
}
