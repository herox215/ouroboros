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
	cache    assistant.Cache
	contents assistant.Contents
	failed   error // of Contents
	inspect  *agent.Inspection
	resumed  map[string]json.RawMessage
	resume   error
	forgot   []string
	title    func(ctx context.Context, conversation []openai.Message) (string, error)
	effort   string // of the node
}

func (a *fakeAgent) Ready() error { return a.ready }
func (a *fakeAgent) Run(ctx context.Context, _, text string, observe func(assistant.Step), say func(string)) (string, error) {
	if a.run != nil {
		return a.run(ctx, text, observe, say)
	}
	return "pong", nil
}
func (a *fakeAgent) Context(string) int                          { return a.size }
func (a *fakeAgent) Cache(string) assistant.Cache                { return a.cache }
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

func (a *fakeAgent) Title(ctx context.Context, conversation []openai.Message) (string, error) {
	if a.title != nil {
		return a.title(ctx, conversation)
	}
	return "", nil
}

func (a *fakeAgent) Effort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effort
}

func (a *fakeAgent) Forget(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forgot = append(a.forgot, id)
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
	a := &fakeAgent{size: 42, cache: assistant.Cache{Tokens: 10, Cached: 5, AllTokens: 30, AllCached: 20}, run: func(_ context.Context, text string, observe func(assistant.Step), say func(string)) (string, error) {
		say("I will look")
		observe(assistant.Step{Tool: "read", Args: json.RawMessage(`{"path":"~/a"}`)})
		observe(assistant.Step{Tool: "read", Args: json.RawMessage(`{"path":"~/a"}`), Done: true, Took: time.Second, Err: errors.New("no such file"), Remark: "said"})
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
	if kinds(got) != "message say step step" || got[0].Text != "where is a" || got[1].Text != "I will look" || got[0].Context != 42 || got[0].Cache != a.cache {
		t.Fatalf("events %+v", got)
	}
	if st := got[3].AsStep(); st.Tool != "read" || !st.Done || st.Took != time.Second || st.Err.Error() != "no such file" || string(st.Args) != `{"path":"~/a"}` || st.Remark != "said" {
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
	long := strings.Repeat("x", 129)
	for _, id := range []string{"", long, "../x", "peer-a/b"} {
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
	if _, _, err := s.Watch(ctx, strings.Repeat("x", 128)); err != nil {
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
	s.node.mu.Lock()
	s.node.release = true
	s.node.mu.Unlock()
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
	s.Steer(ctx, "s1", "late") // nobody answers it any more
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
	if kinds(last) != "message queued canceled" {
		t.Fatalf("events %+v", last)
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

// Titles that fit preserve the accepted first message. The in-process API
// accepts Go strings without a UTF-8 precondition, unlike JSON on the socket.
// A boundary change must not silently sanitize only exactly-full titles.
func TestListPreservesTitlesThatFit(t *testing.T) {
	for _, text := range []string{"short", strings.Repeat("ä", maxTitle/2), strings.Repeat("x", maxTitle-1) + "\xff"} {
		s := service(t, &fakeAgent{})
		_, live, err := s.Watch(t.Context(), "id")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Ask(t.Context(), "id", text); err != nil {
			t.Fatal(err)
		}
		until(t, live, Answer)
		list, err := s.List(t.Context())
		if err != nil || len(list) != 1 || list[0].Title != text {
			t.Fatalf("accepted message %q, titles %+v, error %v", text, list, err)
		}
	}
}

// Truncation may cut a multibyte character. The title must end at the last
// complete character, rather than contain an incomplete UTF-8 sequence.
func TestListTruncatesAtACompleteCharacter(t *testing.T) {
	prefix := strings.Repeat("x", maxTitle-1)
	s := service(t, &fakeAgent{})
	_, live, err := s.Watch(t.Context(), "id")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(t.Context(), "id", prefix+"ä"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	list, err := s.List(t.Context())
	if err != nil || len(list) != 1 || list[0].Title != prefix {
		t.Fatalf("titles %+v, error %v", list, err)
	}
}

// What the owner says while an answer is on its way is queued: the answer
// takes it after the tools it waits for, and what comes too late for that is
// the next message. The last that waits is taken back.
func TestSteer(t *testing.T) {
	tools, late := make(chan struct{}), make(chan struct{})
	var asked []string
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		asked = append(asked, text)
		if text != "a" {
			return "then", nil
		}
		<-tools
		taken := agent.Steered(ctx)
		if again := agent.Steered(ctx); len(again) != 0 {
			return "", errors.New("taken twice")
		}
		<-late
		return "took " + strings.Join(taken, ","), nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if text, err := s.Unsteer(ctx, "s1"); text != "" || err != nil {
		t.Fatalf("nothing waits: %q, %v", text, err)
	}
	if text, err := s.Unsteer(ctx, "nobody"); text != "" || err != nil {
		t.Fatalf("no session: %q, %v", text, err)
	}
	// Without an answer on its way, it is asked.
	if err := s.Steer(ctx, "s1", "a"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"b", "c", "d"} {
		if err := s.Steer(ctx, "s1", text); err != nil {
			t.Fatal(err)
		}
	}
	if text, err := s.Unsteer(ctx, "s1"); text != "d" || err != nil {
		t.Fatalf("taken back %q, %v", text, err)
	}
	close(tools)
	got := until(t, live, Message, Message, Message)
	got = append(got, until(t, live, Message)...)
	got = append(got, until(t, live, Message)...)
	if kinds(got) != "message queued queued queued unqueued message message" || got[4].Text != "d" || got[5].Text != "b" || got[6].Text != "c" {
		t.Fatalf("events %+v", got)
	}
	if text, err := s.Unsteer(ctx, "s1"); text != "" || err != nil {
		t.Fatalf("the answer took them: %q, %v", text, err)
	}
	// Too late for the tools: the next message, all of it at once.
	s.Steer(ctx, "s1", "e")
	s.Steer(ctx, "s1", "f")
	close(late)
	got = until(t, live, Answer)
	got = append(got, until(t, live, Answer)...)
	if kinds(got) != "queued queued answer message message answer" || got[2].Text != "took b,c" || got[3].Text != "e" || got[4].Text != "f" || got[5].Text != "then" {
		t.Fatalf("events %+v", got)
	}
	if strings.Join(asked, "|") != "a|e\n\nf" {
		t.Fatalf("asked %q", asked)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Running {
		t.Fatalf("list %+v", list)
	}
	for id, want := range map[string]error{"": ErrNoSession, "s1": ErrEmpty} {
		if err := s.Steer(ctx, id, " "); !errors.Is(err, want) {
			t.Fatalf("steer %q: %v", id, err)
		}
	}
	if _, err := s.Unsteer(ctx, ""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unsteer: %v", err)
	}
}

// What waits when ouro is to start anew is not answered by the build that
// goes: the node takes no message until then.
func TestQueueIsLostWhenRestarting(t *testing.T) {
	gate := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		<-gate
		return "built", nil
	}}
	s := service(t, a)
	replaced := make(chan string, 8)
	s.Attach(nil, func(exe string) { replaced <- exe })
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "build")
	s.Steer(ctx, "s1", "late")
	close(gate)
	if got := until(t, live, Restart); kinds(got) != "message queued answer restart" {
		t.Fatalf("events %+v", got)
	}
	if exe := <-replaced; exe != "/new/ouro" {
		t.Fatalf("replaced by %q", exe)
	}
	if past, _, _ := s.Watch(ctx, "s1"); kinds(past) != "message queued answer restart" {
		t.Fatalf("past %+v", past)
	}
}

// What the review of an answer proposes comes after the end of the answer,
// also when it is in before.
func TestSuggestionAfterTheAnswer(t *testing.T) {
	later := make(chan struct{})
	s := New(&fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		suggest := assistant.Reviewer(ctx)
		suggest("early")
		go func() {
			<-later
			suggest("late")
		}()
		return "pong", nil
	}})
	defer s.Close()
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "hi")
	got := until(t, live, Suggestion)
	if kinds(got) != "message answer suggestion" || got[2].Text != "early" {
		t.Fatalf("events %+v", got)
	}
	close(later)
	if got := until(t, live, Suggestion); kinds(got) != "suggestion" || got[0].Text != "late" {
		t.Fatalf("events %+v", got)
	}
}
