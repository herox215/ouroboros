package session

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// clock is a time a test moves on, which a Service reads as its now.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) move(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// dispatcher is an agent that does in the thread what a message says, and
// tells told each text the thread is asked: "new TEXT" starts a session with
// TEXT, "send ID TEXT" hands TEXT to the session ID, "pass ID" hands it what
// the owner said, and either answers nothing, as the session answers; what
// cannot be handed on it answers with why. Elsewhere it answers as answer does, "pong:TEXT" without one.
func dispatcher(told chan<- string, answer func(ctx context.Context, text string) (string, error)) *fakeAgent {
	return &fakeAgent{run: func(ctx context.Context, text string, observe func(assistant.Step), say func(string)) (string, error) {
		thread := assistant.ThreadOf(ctx)
		if thread == nil {
			if answer != nil {
				return answer(ctx, text)
			}
			return "pong:" + text, nil
		}
		if strings.HasPrefix(text, backgroundHead) {
			_, result, _ := strings.Cut(text, "\n\n")
			return result, nil // fixture represents the thread composing its reply
		}
		told <- text
		// What it does on its way is not shown.
		say("handing it on")
		observe(assistant.Step{Tool: assistant.ToolSessionSend})
		verb, rest, _ := strings.Cut(text, " ")
		var err error
		switch verb {
		case "new":
			_, err = thread.Start(ctx, rest, "")
		case "send":
			id, message, _ := strings.Cut(rest, " ")
			_, _, err = thread.Send(ctx, id, message)
		case "pass":
			_, _, err = thread.Send(ctx, rest, "")
		default:
			return "noted " + text, nil
		}
		if err != nil {
			return "refused: " + err.Error(), nil
		}
		return "", nil
	}}
}

// through returns past, and what comes on live after it until an Event of
// kind came, unless one is in past already.
func through(t *testing.T, past []Event, live <-chan Event, kind Kind) []Event {
	t.Helper()
	if slices.ContainsFunc(past, func(e Event) bool { return e.Kind == kind }) {
		return past
	}
	return append(past, until(t, live, kind)...)
}

// ask asks text in the thread of s, from the terminal UI.
func ask(t *testing.T, s *Service, text string) {
	t.Helper()
	if err := s.Ask(WithMessageSource(context.Background(), MessageSource{Interface: "tui"}), ThreadID, text); err != nil {
		t.Fatal(err)
	}
}

// working reads live until nothing works for the thread any more, and
// returns what came on the way.
func working(t *testing.T, live <-chan Event) []Event {
	t.Helper()
	var got []Event
	for {
		got = append(got, until(t, live, Working)...)
		if got[len(got)-1].N == 0 {
			return got
		}
	}
}

// upTo reads live until Working told ns, one after the other, and returns
// what came on the way.
func upTo(t *testing.T, live <-chan Event, ns ...int) []Event {
	t.Helper()
	var got []Event
	for _, n := range ns {
		got = append(got, until(t, live, Working)...)
		if got[len(got)-1].N != n {
			t.Fatalf("working %d, not %d, after %v", got[len(got)-1].N, n, kinds(got))
		}
	}
	return got
}

// shown is what of events the owner sees in the chat: the messages, the
// answers, why one failed, and the questions with their answers.
func shown(events []Event) []string {
	var lines []string
	for _, e := range events {
		switch e.Kind {
		case Message, Answer, Failed:
			lines = append(lines, string(e.Kind)+" "+e.Text)
		case Asked:
			if e.Done {
				lines = append(lines, "answered "+e.Text)
			} else {
				lines = append(lines, "asked "+e.AsQuestion().Question)
			}
		case Say, Step, Thought, Answering, Calling:
			lines = append(lines, "along "+string(e.Kind))
		}
	}
	return lines
}

// The thread starts a session for what is new, and says nothing of it: the
// session answers on its own, with what the owner wrote it from, and its
// answer is shown as the answer of the thread, which is told it for its next
// answer, kept as the occurrence it was shown as. Meanwhile the thread works.
// It is no session to list, and is not named for a topic.
func TestThreadStartsASession(t *testing.T) {
	told := make(chan string, 4)
	gate := make(chan struct{})
	a := dispatcher(told, func(_ context.Context, text string) (string, error) {
		<-gate
		return "pong:" + text, nil
	})
	a.title = func(context.Context, []openai.Message) (string, error) { return "Login", nil }
	s := service(t, a)
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	ask(t, s, "new fix the login")
	if <-told != "new fix the login" {
		t.Fatal("the thread was not asked")
	}
	got := upTo(t, thread, 1, 2, 1) // the thread thinks, the session works with it, the thread is done
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	id := list[0].ID
	past, _, _ := s.Watch(ctx, ThreadID)
	if last := past[len(past)-1]; last.Kind != Working || last.N != 1 {
		t.Fatalf("who watches now is not told that it works: %+v", last)
	}
	close(gate)
	got = append(got, working(t, thread)...)
	if lines := shown(got); !slices.Equal(lines, []string{"message new fix the login", "answer pong:fix the login"}) {
		t.Fatalf("the thread showed %q", lines)
	}
	answer := got[slices.IndexFunc(got, func(e Event) bool { return e.Kind == Answer })]
	if answer.PresentationID == "" {
		t.Fatalf("the answer %+v", answer)
	}
	past, live, _ := s.Watch(ctx, id)
	if got := through(t, past, live, Answer); got[0].Kind != Message || got[0].Text != "fix the login" || got[0].Source != (MessageSource{Interface: "tui"}) || !got[0].Routed {
		t.Fatalf("the session: %v", kinds(got))
	}
	if notes := a.noted(); len(notes) != 0 {
		t.Fatalf("completion must be processed, not merely noted: %q", notes)
	}
	past, _, _ = s.Watch(ctx, ThreadID)
	if slices.ContainsFunc(past, func(e Event) bool { return e.Kind == Title }) {
		t.Fatalf("the thread was named: %+v", past)
	}
}

// The thread hands a message to a session of the last day, which answers it
// as a message of the owner: the answer is shown in the thread. What the
// owner writes in the session directly is not. A session that is none of
// those of the last day, or the thread itself, is refused, and the thread
// says why itself.
func TestThreadSendsToASession(t *testing.T) {
	told := make(chan string, 8)
	at := &clock{at: time.Now()}
	s := service(t, dispatcher(told, nil))
	s.now = at.now
	ctx := context.Background()
	asked(t, s, "s1", "hello")
	s.mu.Lock()
	quiet := s.sessions[ThreadID] == nil
	s.mu.Unlock()
	if !quiet {
		t.Fatal("the thread was told of an answer to the owner")
	}
	_, thread, _ := s.Watch(ctx, ThreadID)
	ask(t, s, "send s1 more")
	<-told
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"message send s1 more", "answer pong:more"}) {
		t.Fatalf("the thread showed %q", lines)
	}
	if got := asked(t, s, "s1", "directly"); got[0].Routed {
		t.Fatalf("what the owner wrote in the session is handed on: %+v", got[0])
	}
	for _, id := range []string{"nope", ThreadID} {
		ask(t, s, "send "+id+" more")
		<-told
		want := fmt.Sprintf("answer refused: no session %q among those of the last 24 hours: "+
			"hand the message to one of the list, or start a new one with session_new", id)
		if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"message send " + id + " more", want}) {
			t.Fatalf("%s: %q", id, lines)
		}
	}
	// Without words of its own the thread hands on those of the owner.
	ask(t, s, "pass s1")
	<-told
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"message pass s1", "answer pong:pass s1"}) {
		t.Fatalf("the words of the owner: %q", lines)
	}
	at.move(recent + time.Minute)
	asked(t, s, "s2", "fresh")
	ask(t, s, "send s1 more")
	<-told
	if lines := shown(working(t, thread)); !strings.HasPrefix(lines[1], `answer refused: no session "s1"`) {
		t.Fatalf("a session of more than a day ago: %q", lines)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if list := s.routable(); len(list) != 1 || list[0].id != "s2" {
		t.Fatalf("routable %+v", list)
	}
}

// A message the thread hands to a session that works is queued there, and
// only the answer that takes it is shown.
func TestThreadQueues(t *testing.T) {
	told := make(chan string, 8)
	gate := make(chan struct{})
	s := service(t, dispatcher(told, func(_ context.Context, text string) (string, error) {
		if text == "wait" {
			<-gate
		}
		return "pong:" + text, nil
	}))
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	_, s1, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "wait"); err != nil {
		t.Fatal(err)
	}
	until(t, s1, Message)
	ask(t, s, "send s1 more")
	<-told
	if got := until(t, s1, Queued); got[0].Text != "more" || got[0].Source != (MessageSource{Interface: "tui"}) || !got[0].Routed {
		t.Fatalf("queued %+v", got)
	}
	// The queued message works for the thread, the answer before it not.
	upTo(t, thread, 1, 2, 1)
	close(gate)
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"answer pong:more"}) {
		t.Fatalf("only the answer that took the message is shown: %q", lines)
	}
}

// question reads live until a question is asked that waits for its
// answer, and returns what came on the way.
func question(t *testing.T, live <-chan Event) []Event {
	t.Helper()
	var got []Event
	for {
		got = append(got, until(t, live, Asked)...)
		if !got[len(got)-1].Done {
			return got
		}
	}
}

// What a session asks the owner on the way of an answer the thread handed a
// message to is asked in the thread, one question at a time: what the owner
// says there next answers it, and the thread is told both. A question the
// owner answers in the session itself is over in the thread too, and one
// the thread did not ask yet is never asked there. A question of the owner's
// own answer is not asked in the thread.
func TestThreadAsks(t *testing.T) {
	told := make(chan string, 8)
	asks := json.RawMessage(`{"question":"Which?","options":["a","b"],"recommended":"a"}`)
	a := dispatcher(told, func(ctx context.Context, text string) (string, error) {
		answer, err := agent.AskOwner(ctx, asks)
		return "chose " + answer, err
	})
	s := service(t, a)
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	_, s1, _ := s.Watch(ctx, "s1")
	_, s2, _ := s.Watch(ctx, "s2")
	for i, live := range []<-chan Event{s1, s2} {
		id, text := fmt.Sprintf("s%d", i+1), []string{"first", "second"}[i]
		if err := s.Ask(ctx, id, text); err != nil {
			t.Fatal(err)
		}
		question(t, live)
		s.mu.Lock()
		routes := s.routable()
		s.mu.Unlock()
		if routes[0].id != id || routes[0].question != "Which?" || !routes[0].running {
			t.Fatalf("routable %+v", routes)
		}
		s.Steer(ctx, id, "a")
		until(t, live, Answer)
	}
	both := func(text string) {
		t.Helper()
		ask(t, s, "send s1 "+text)
		<-told
		question(t, s1)
		ask(t, s, "send s2 "+text)
		<-told
		question(t, s2)
	}

	// Two sessions ask at once: the thread asks the first, and the second
	// once the first is answered.
	both("go")
	got := question(t, thread)
	if err := s.Steer(WithMessageSource(ctx, MessageSource{Interface: "tui"}), ThreadID, "b"); err != nil {
		t.Fatal(err)
	}
	got = append(got, question(t, thread)...)
	until(t, s1, Answer)
	if err := s.Steer(ctx, "s2", "a"); err != nil { // in the session itself
		t.Fatal(err)
	}
	until(t, s2, Answer)
	got = append(got, working(t, thread)...)
	lines := shown(got)
	for _, want := range []string{"asked Which?", "answered b", "answer chose b", "answered a", "answer chose a"} {
		if !slices.Contains(lines, want) {
			t.Fatalf("the thread showed %q", lines)
		}
	}
	if n := len(slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return l != "asked Which?" })); n != 2 {
		t.Fatalf("the thread asked %d times: %q", n, lines)
	}
	notes := a.noted()
	i := slices.IndexFunc(notes, func(n string) bool {
		return strings.HasPrefix(n, `thread: [Session s1 "first" asked the owner, as you, and they answered. The question, then the answer:]`) &&
			strings.HasSuffix(n, string(asks)+"\n\nb")
	})
	if i < 0 {
		t.Fatalf("the thread was told %q", notes)
	}
	asked := got[slices.IndexFunc(got, func(e Event) bool { return e.Kind == Asked })]
	if presented := a.presentedAs(); presented[i] != asked.PresentationID || asked.PresentationID == "" {
		t.Fatalf("the question was shown as %q, and kept as %q", asked.PresentationID, presented[i])
	}

	// A question not asked in the thread yet ends there without a word.
	both("again")
	question(t, thread)
	if err := s.Steer(ctx, "s2", "b"); err != nil {
		t.Fatal(err)
	}
	until(t, s2, Answer)
	if err := s.Steer(ctx, ThreadID, "a"); err != nil {
		t.Fatal(err)
	}
	until(t, s1, Answer)
	lines = shown(working(t, thread))
	if !slices.Contains(lines, "answered a") || !slices.Contains(lines, "answer chose b") || slices.Contains(lines, "asked Which?") {
		t.Fatalf("the thread showed %q", lines)
	}
}

// What the thread hands to a session that waits for the answer to a
// question of the owner's own answer answers it, and the answer that goes
// on with it is shown in the thread.
func TestThreadAnswersTheSession(t *testing.T) {
	told := make(chan string, 4)
	asks := json.RawMessage(`{"question":"Which?","options":["a","b"]}`)
	s := service(t, dispatcher(told, func(ctx context.Context, text string) (string, error) {
		answer, err := agent.AskOwner(ctx, asks)
		return "chose " + answer, err
	}))
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	_, s1, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "first"); err != nil {
		t.Fatal(err)
	}
	question(t, s1)
	ask(t, s, "send s1 b")
	<-told
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"message send s1 b", "answer chose b"}) {
		t.Fatalf("the thread showed %q", lines)
	}
}

// Of an answer that failed the thread shows why; of one that was stopped it
// shows nothing. Stopping the thread stops what works for it.
func TestThreadShowsTheEnd(t *testing.T) {
	told := make(chan string, 8)
	a := dispatcher(told, func(ctx context.Context, text string) (string, error) {
		switch text {
		case "fail":
			return "", errTest
		case "stop":
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "pong:" + text, nil
	})
	a.note = errTest
	s, _, _ := archived(t, a, t.TempDir(), time.Now())
	ctx := context.Background()
	asked(t, s, "s1", "hello")
	_, thread, _ := s.Watch(ctx, ThreadID)
	_, s1, _ := s.Watch(ctx, "s1")
	ask(t, s, "send s1 fail")
	<-told
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"message send s1 fail", "answer " + errTest.Error()}) {
		t.Fatalf("the thread showed %q", lines)
	}
	if notes := a.noted(); len(notes) != 0 {
		t.Fatalf("failure bypassed conversational processing: %q", notes)
	}

	ask(t, s, "send s1 stop")
	<-told
	for got := until(t, s1, Message); got[len(got)-1].Text != "stop"; {
		got = until(t, s1, Message) // of the answer before
	}
	if err := s.Cancel(ctx, ThreadID); err != nil {
		t.Fatal(err)
	}
	until(t, s1, Canceled)
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"message send s1 stop"}) {
		t.Fatalf("the thread showed %q", lines)
	}
}

// A session the thread starts works in the project of the thread, or in the
// directory it names; one it cannot start is not kept. The sessions here
// answer until the node stops.
func TestThreadStartsInItsProject(t *testing.T) {
	project, other := canonicalTempDir(t), canonicalTempDir(t)
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	s := service(t, a)
	ctx := context.Background()
	start := threading{s}.Start
	if err := s.SetDirectory(ctx, ThreadID, project); err != nil {
		t.Fatal(err)
	}
	for directory, want := range map[string]string{"": project, other: other} {
		id, err := start(ctx, "go", directory)
		if err != nil || directoryAt(t, s, id) != want {
			t.Fatalf("%q: %s, %v", directory, id, err)
		}
	}
	if _, err := start(ctx, " ", ""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("an empty task: %v", err)
	}
	if _, err := start(ctx, "go", "/no/such/directory"); err == nil {
		t.Fatal("a session in a directory that is not")
	}
	a.ready = errTest
	if _, err := start(ctx, "go", ""); !errors.Is(err, errTest) {
		t.Fatalf("an agent that cannot answer: %v", err)
	}
	a.ready = nil
	if list, _ := s.List(ctx); len(list) != 2 {
		t.Fatalf("a session that could not start is kept: %+v", list)
	}
	s.Close()
	if _, err := start(ctx, "go", ""); !errors.Is(err, ErrStopped) {
		t.Fatalf("a node that stops: %v", err)
	}
}

// nothing has nothing against a message the thread hands on.
func nothing() error { return nil }

// What is handed on goes nowhere the node does not take a message: when the
// agent cannot answer, the node stops or starts anew. Where the owner wrote
// nothing in the thread, they wrote it in a way that is not known. Without a
// thread, nothing is asked or told in it.
func TestHandRefuses(t *testing.T) {
	a := &fakeAgent{}
	s := service(t, a)
	ctx := context.Background()
	s.Watch(ctx, ThreadID)
	_, live, _ := s.Watch(ctx, "s1")
	if _, _, err := s.hand(ctx, "s1", "", nil, nothing); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if _, _, err := s.hand(ctx, "s1", "go", nil, nothing); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	s.mu.Lock()
	source := s.sessions["s1"].events[0].Source
	s.mu.Unlock()
	if source != (MessageSource{Interface: "unknown"}) {
		t.Fatalf("source %+v", source)
	}
	a.ready = errTest
	if _, _, err := s.hand(ctx, "s1", "go", nil, nothing); !errors.Is(err, errTest) {
		t.Fatalf("not ready: %v", err)
	}
	a.ready = nil
	s.node.mu.Lock()
	s.node.successor = "/new/ouro"
	s.node.mu.Unlock()
	if _, _, err := s.hand(ctx, "s1", "go", nil, nothing); !errors.Is(err, ErrRestarting) {
		t.Fatalf("restarting: %v", err)
	}
	s.node.mu.Lock()
	s.node.successor = ""
	s.node.mu.Unlock()
	s.Close()
	if _, _, err := s.hand(ctx, "s1", "go", nil, nothing); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answerRelayed("a", MessageSource{}) {
		t.Fatal("the thread asks no question")
	}
	delete(s.sessions, ThreadID)
	s.answered(s.get("s1"), queued{})
	s.busy()
	if s.answerRelayed("a", MessageSource{}) || s.sessions[ThreadID] != nil {
		t.Fatal("there is no thread")
	}
}

// The thread is told of the sessions of the last day the node keeps, as
// well as of those it has: with their project, never of one in which nothing
// was said, and at most maxRouted of them.
func TestRoutable(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	arch.Append(history.Who{Session: "kept"}, []openai.Message{{Role: "user", Content: "from before"}}, nil)
	arch.SetDirectory(history.Owner("kept"), "/project")
	arch.Append(history.Who{Session: ThreadID}, []openai.Message{{Role: "user", Content: "of the thread"}}, nil)
	arch.Append(history.Who{Session: "p1", Peer: "key"}, []openai.Message{{Role: "user", Content: "from a peer"}}, nil)
	arch.SetDirectory(history.Owner("empty"), "/project")
	arch.Close()
	at := &clock{at: time.Now().Add(time.Hour)}
	s, _, l := archived(t, &fakeAgent{}, dir, at.now())
	s.now = at.now
	asked(t, s, "here", "hi")
	s.mu.Lock()
	got := s.routable()
	s.mu.Unlock()
	if len(got) != 2 || got[0].id != "here" || got[0].title != "hi" || got[1].id != "kept" || got[1].title != "from before" || got[1].directory != "/project" {
		t.Fatalf("routable %+v", got)
	}
	at.move(recent)
	s.mu.Lock()
	got = s.routable()
	s.mu.Unlock()
	if len(got) != 1 || got[0].id != "here" {
		t.Fatalf("a day later %+v", got)
	}

	// The latest first, and those of the same time by their ID.
	s.mu.Lock()
	for i := range maxRouted + 1 {
		id := fmt.Sprintf("s%02d", i)
		s.sessions[id] = &session{id: id, events: []Event{{Kind: Message, Text: id, At: at.now()}}}
	}
	got = s.routable()
	s.archive.Archive = unavailableArchive(t)
	s.routable()
	s.mu.Unlock()
	if len(got) != maxRouted || got[0].id != "s00" || got[maxRouted-1].id != fmt.Sprintf("s%02d", maxRouted-1) {
		t.Fatalf("%d routable, %+v first", len(got), got[0])
	}
	if !l.has("the archive cannot be listed for the thread") {
		t.Fatalf("log %q", l.lines)
	}
}

// roster tells each session on a line of its own, by its handle, with its
// project where it is not that of the thread, and what it does: nothing that
// changes by itself, as the time does.
func TestRoster(t *testing.T) {
	now := time.Now()
	if got := roster(nil, ""); got != "[Sessions of the owner in the last 24 hours: none. ouro tells them whenever they change; the owner did not write this.]" {
		t.Fatalf("no session: %q", got)
	}
	got := roster([]routed{
		{id: "7f3a9c12-0000-4000-8000-000000000000", title: "Fix\nthe login", directory: "/app", last: now.Add(-2 * time.Minute), running: true},
		{id: "b", title: `Sort "photos"`, last: now.Add(-3 * time.Hour), running: true, question: "Which\nfolder?"},
		{id: "c", title: "Done", directory: "/other", last: now},
	}, "/app")
	want := "[Sessions of the owner in the last 24 hours, the latest first, as ouro tells them whenever they change; the owner did not write it:\n" +
		"- 7f3a9c12: \"Fix the login\" · working\n" +
		"- b: \"Sort \\\"photos\\\"\" · in your workspace · asks the owner and waits: \"Which folder?\"\n" +
		"- c: \"Done\" · in /other\n]"
	if got != want || !isRoster(got) || isRoster("hello") {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// The thread is told the sessions before the message of the owner where
// they changed since it was told them last, or rosterEvery answers ago, and
// what it is told stays; another session is told no list.
func TestInThread(t *testing.T) {
	s := service(t, &fakeAgent{})
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := []openai.Message{{Role: "user", Content: "hi"}}
	if got := s.inThread(s.get("s1"), messages); len(got) != 1 {
		t.Fatalf("a session is told the sessions: %+v", got)
	}
	thread := s.get(ThreadID)
	told := func() int {
		n := 0
		for range rosterEvery * 2 {
			if got := s.inThread(thread, messages); len(got) == 2 && isRoster(got[0].Content) && got[1].Content == "hi" {
				n++
			}
		}
		return n
	}
	if n := told(); n != 2 {
		t.Fatalf("told the same sessions %d times in %d answers", n, rosterEvery*2)
	}
	s.sessions["s2"] = &session{id: "s2", events: []Event{{Kind: Message, Text: "new", At: s.now()}}}
	if got := s.inThread(thread, messages); len(got) != 2 || !strings.Contains(got[0].Content, `- s2: "new"`) {
		t.Fatalf("a new session is not told: %+v", got)
	}
}

// The agent of the thread is sent of long answers of sessions their start
// and end, the same each time, and the messages of the owner without where
// they wrote them; all else as it is, and none is left out, so that each
// call begins with what the one before sent.
func TestShaped(t *testing.T) {
	se := &session{id: "s1", title: "Fix"}
	// Cut within a character at either end, which is left out.
	long := strings.Repeat("x", relayKept-1) + "ä" + strings.Repeat("z", 100) + "ö" + strings.Repeat("y", relayKept-1)
	short := relayText(se, relayAnswered, "fixed")
	failed := relayText(se, relayFailed, "it broke]")
	call := openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{call("c1", assistant.ToolSessionSend, `{}`)}}
	tui := MessageSource{Interface: "tui"}
	msgs := []openai.Message{
		{Role: "user", Content: roster(nil, "")},
		{Role: "user", Content: "fix it", Source: tui},
		call,
		{Role: "tool", ToolCallID: "c1", Content: "Handed on."},
		{Role: "user", Content: relayText(se, relayAnswered, long)},
		{Role: "user", Content: short},
		{Role: "user", Content: failed},
		{Role: "assistant", Content: relayText(se, relayAnswered, long)},
	}
	got := shaped(msgs)
	want := relayText(se, relayAnswered, strings.Repeat("x", relayKept-1)+relayGap+strings.Repeat("y", relayKept-1))
	if len(got) != len(msgs) || got[0].Content != msgs[0].Content || got[1].Content != "fix it" || got[1].Source != (MessageSource{}) ||
		len(got[2].ToolCalls) != 1 || got[3].ToolCallID != "c1" || got[4].Content != want || got[5].Content != short ||
		got[6].Content != failed || got[7].Content != msgs[7].Content {
		t.Fatalf("shaped %+v", got)
	}
	if again := shaped(msgs); again[4].Content != want || msgs[4].Content != relayText(se, relayAnswered, long) || msgs[1].Source != tui {
		t.Fatalf("shaped anew %q, kept %q", again[4].Content, msgs[4].Content)
	}
}

// The agent of the thread is sent what happened in it within the last
// assistant.ThreadWindow, cut before a message of the owner or of ouro, and the
// last list of the sessions it was told before, unless it is told one anew.
func TestRecentOf(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	old, recent := now.Add(-assistant.ThreadWindow-time.Minute), now.Add(-assistant.ThreadWindow+time.Minute)
	sessions := roster(nil, "")
	msgs := []openai.Message{
		{Role: "user", Content: sessions, At: old},
		{Role: "user", Content: "fix it", At: old},
		{Role: "assistant", ToolCalls: []openai.ToolCall{call("c1", assistant.ToolSessionSend, `{}`)}, At: old},
		{Role: "tool", ToolCallID: "c1", Content: "Handed on.", At: recent}, // its answer ended late
		{Role: "user", Content: "and this", At: recent},
		{Role: "user", Content: "now"}, // of the answer on its way
	}
	contents := func(msgs []openai.Message) []string {
		var out []string
		for _, m := range msgs {
			out = append(out, cmp.Or(m.Content, m.Role))
		}
		return out
	}
	if got := contents(recentOf(msgs, now)); !slices.Equal(got, []string{sessions, "and this", "now"}) {
		t.Fatalf("recent %q", got)
	}
	if got := contents(recentOf(msgs, recent)); !slices.Equal(got, contents(msgs)) {
		t.Fatalf("all recent %q", got)
	}
	if got := contents(recentOf(msgs[:4], now)); !slices.Equal(got, []string{sessions}) {
		t.Fatalf("nothing recent %q", got)
	}
	if got := contents(recentOf(msgs[1:], now)); !slices.Equal(got, []string{"and this", "now"}) {
		t.Fatalf("without a list %q", got)
	}
	told := append(slices.Clone(msgs), openai.Message{Role: "user", Content: roster([]routed{{id: "s1", title: "Fix"}}, "")})
	if got := recentOf(told, now); len(got) != 3 || got[0].Content != "and this" {
		t.Fatalf("told anew %q", contents(got))
	}
}

// What the thread is told of a session reads back as what it showed of it.
func TestRelayText(t *testing.T) {
	se := &session{id: "s1", title: `Fix "it"`}
	question := `{"question":"Which?","options":["a","b"]}`
	for text, want := range map[string][]string{
		relayText(se, relayAnswered, "done\n\nreally"):      {"answer done\n\nreally"},
		relayText(se, relayFailed, "it broke]"):             {"failed it broke"},
		relayText(se, relayAsked, question+"\n\nb\n\nor a"): {"asked Which?", "answered b\n\nor a"},
	} {
		events, ok := asRelay(text)
		if !ok || !slices.Equal(shown(events), want) {
			t.Fatalf("%q: %q, %v", text, shown(events), ok)
		}
	}
	for _, text := range []string{
		"[Task 1, which you delegated",
		"[Session s1",
		"[Session s1 Fix",
		`[Session s1 "Fix" slept.`,
		`[Session s1 "Fix"` + relayFailed + "why",
		`[Session s1 "Fix"` + relayAsked + `{not json` + "\n\nb",
		`[Session s1 "Fix"` + relayAsked + question,
	} {
		if events, ok := asRelay(text); ok {
			t.Fatalf("%q read as %+v", text, events)
		}
	}
}

// The thread goes on from the archive as it was shown: what it was told of
// the sessions is no message of the owner, what it did on its way is not
// shown, and what the sessions answered and asked shows as before. The
// thread is none of the sessions listed or found.
func TestThreadArchived(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	se := &session{id: "s1", title: "Fix"}
	arch.Append(history.Who{Session: ThreadID}, []openai.Message{
		{Role: "user", Content: resumed(time.Now(), time.Now()) + roster(nil, "")},
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "handing on", ToolCalls: []openai.ToolCall{call("c1", assistant.ToolSessionSend, `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: "Handed to the session s1"},
	}, nil)
	arch.Append(history.Who{Session: ThreadID}, []openai.Message{{Role: "user", Content: relayText(se, relayAnswered, "fixed")}}, nil)
	arch.Append(history.Who{Session: ThreadID}, []openai.Message{{Role: "user", Content: relayText(se, relayAsked, `{"question":"Which?","options":["a","b"]}`+"\n\na")}}, nil)
	arch.Close()
	a := &fakeAgent{}
	s, _, _ := archived(t, a, dir, time.Now())
	ctx := context.Background()
	past, _, _ := s.Watch(ctx, ThreadID)
	if lines := shown(past); !slices.Equal(lines, []string{"message fix it", "answer fixed", "asked Which?", "answered a"}) || past[1].At.IsZero() || past[1].PresentationID == "" {
		t.Fatalf("past %q: %+v", lines, past)
	}
	var memory []openai.Message
	json.Unmarshal(a.resumed[ThreadID], &memory)
	if len(memory) != 6 || !isRoster(unnoted(memory[0].Content)) || slices.ContainsFunc(memory, func(m openai.Message) bool { return m.At.IsZero() }) {
		t.Fatalf("the agent remembers %+v", memory)
	}
	// It searches all of it, also what its agent is no longer sent.
	if found, err := (threading{s}).Search(ctx, "FIX IT"); err != nil || !strings.Contains(found, "user: fix it") || !strings.Contains(found, "[1 hit in this conversation]") {
		t.Fatalf("searched %q, %v", found, err)
	}
	list, _ := s.List(ctx)
	found, _ := s.Find(ctx, "fix")
	if len(list) != 0 || len(found) != 0 {
		t.Fatalf("list %+v, found %q", list, found)
	}
	running, _ := (&Service{sessions: map[string]*session{ThreadID: {id: ThreadID, cancel: func() {}}}}).RunningSessions(ctx)
	if len(running) != 1 || running[0] != "Thread" {
		t.Fatalf("running %q", running)
	}
}

// Where the agent of the thread gives no words of its own, a session gets
// all the owner said to the answer of the thread, as they said it; the
// pictures of it it gets either way.
func TestWords(t *testing.T) {
	s := service(t, &fakeAgent{})
	if got, pictures := s.words(""); got != "" || pictures != nil {
		t.Fatalf("of a thread without a message: %q, %v", got, pictures)
	}
	picture := openai.Image{Type: "image/png", Data: []byte("p")}
	s.mu.Lock()
	s.get(ThreadID).said = []string{"fix the login", "and the tests"}
	s.get(ThreadID).pictures = []openai.Image{picture}
	s.mu.Unlock()
	if got, pictures := s.words(" "); got != "fix the login\n\nand the tests" || len(pictures) != 1 {
		t.Fatalf("the words of the owner: %q, %v", got, pictures)
	}
	if got, pictures := s.words("only the tests"); got != "only the tests" || len(pictures) != 1 {
		t.Fatalf("the words of the agent: %q, %v", got, pictures)
	}
}

// What the owner asks in the thread while its agent hands on is queued, and
// answers no question the thread asks: only Steer answers one. A picture of
// the owner goes with what the thread hands on, and a picture alone is
// handed on as well.
func TestThreadTakesItAll(t *testing.T) {
	told := make(chan string, 4)
	gate := make(chan struct{})
	a := dispatcher(told, nil)
	run := a.run
	a.run = func(ctx context.Context, text string, observe func(assistant.Step), say func(string)) (string, error) {
		if text == "wait" {
			<-gate
			return "waited", nil
		}
		return run(ctx, text, observe, say)
	}
	s := service(t, a)
	ctx := WithMessageSource(context.Background(), MessageSource{Interface: "tui"})
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, ThreadID, "wait"); err != nil {
		t.Fatal(err)
	}
	until(t, thread, Message)
	if err := s.Ask(ctx, ThreadID, "noted"); err != nil {
		t.Fatalf("the thread is busy: %v", err)
	}
	if got := until(t, thread, Queued); got[len(got)-1].Text != "noted" || got[len(got)-1].Source != (MessageSource{Interface: "tui"}) {
		t.Fatalf("queued %+v", got)
	}
	close(gate)
	if lines := shown(working(t, thread)); !slices.Equal(lines, []string{"answer waited", "message noted", "answer noted noted"}) {
		t.Fatalf("the thread showed %q", lines)
	}

	picture := openai.Image{Type: "image/png", Data: []byte("p")}
	asked(t, s, "s1", "hello")
	if err := s.AskImages(ctx, ThreadID, "pass s1", []openai.Image{picture}); err != nil {
		t.Fatal(err)
	}
	working(t, thread)
	s.mu.Lock()
	pictures := s.sessions["s1"].pictures
	s.mu.Unlock()
	if len(pictures) != 1 || pictures[0].Type != "image/png" {
		t.Fatalf("the session got the pictures %v", pictures)
	}
	if _, _, err := s.hand(ctx, "s1", " ", []openai.Image{picture}, nothing); err != nil {
		t.Fatalf("a picture alone: %v", err)
	}
}

// The thread thinks little unless the owner wants more, and says so to who
// watches it.
func TestThreadEffort(t *testing.T) {
	s := service(t, &fakeAgent{effort: "high"})
	ctx := context.Background()
	past, _, _ := s.Watch(ctx, ThreadID)
	s.mu.Lock()
	thread, other := s.get(ThreadID), s.get("s1")
	s.mu.Unlock()
	if s.effort(thread) != threadEffort || s.effort(other) != "high" || past[len(past)-1].Kind != Effort || past[len(past)-1].Text != threadEffort {
		t.Fatalf("efforts %q, %q, past %+v", s.effort(thread), s.effort(other), past)
	}
	if err := s.SetEffort(ctx, ThreadID, "max"); err != nil || s.effort(thread) != "max" {
		t.Fatalf("set: %v, %q", err, s.effort(thread))
	}
}

// A session is named by its handle as well as by its ID; a start that names
// more than one names none.
func TestResolve(t *testing.T) {
	s := service(t, &fakeAgent{})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range []string{"7f3a9c12-aaaa", "7f3a9c12-bbbb", "91bc0e45-cccc"} {
		s.sessions[id] = &session{id: id, events: []Event{{Kind: Message, Text: id, At: s.now()}}}
	}
	for id, want := range map[string]string{"91bc0e45": "91bc0e45-cccc", "7f3a9c12-aaaa": "7f3a9c12-aaaa", "7f3a9c12": "7f3a9c12", "nope": "nope"} {
		if got := s.resolve(id); got != want {
			t.Fatalf("%s: %s, want %s", id, got, want)
		}
	}
	if got := handle("7f3a9c12-aaaa"); got != "7f3a9c12" {
		t.Fatalf("handle %q", got)
	}
}
