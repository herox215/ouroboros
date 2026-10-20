package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// namer is the part of a fakeAgent that names sessions: every call is
// handed to calls, and answered with what comes back on its reply.
type namer struct {
	mu    sync.Mutex
	count int
	calls chan naming
}

// naming is one call to name a session.
type naming struct {
	ctx          context.Context
	conversation []openai.Message
	reply        chan titled
}

// titled is what a naming is answered with.
type titled struct {
	title string
	err   error
}

func newNamer() *namer { return &namer{calls: make(chan naming, 16)} }

func (n *namer) title(ctx context.Context, conversation []openai.Message) (string, error) {
	n.mu.Lock()
	n.count++
	n.mu.Unlock()
	c := naming{ctx, conversation, make(chan titled, 1)}
	n.calls <- c
	got := <-c.reply
	return got.title, got.err
}

// next is the next call to name a session.
func (n *namer) next(t *testing.T) naming {
	t.Helper()
	select {
	case c := <-n.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("the agent was not asked for a title")
		return naming{}
	}
}

// asked is how often the agent was asked for a title.
func (n *namer) asked() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count
}

// roles are the roles and texts of a conversation.
func roles(msgs []openai.Message) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+":"+m.Content)
	}
	return strings.Join(out, " ")
}

// A session is named for its topic once the agent can tell it: with every
// message, while the answer runs, the agent is asked with what was said, and
// the answer ends once it named it, or not. The title is told to who watches
// the session, listed and kept along with it.
func TestTitleOnceThereIsATopic(t *testing.T) {
	if titleTime != 20*time.Second {
		t.Fatalf("titleTime is %v; the deadline below is written for 20 seconds", titleTime)
	}
	dir := t.TempDir()
	n := newNamer()
	s, arch, l := archived(t, &fakeAgent{title: n.title}, dir, time.Now())
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")

	s.Ask(ctx, "s1", "hi")
	c := n.next(t)
	if roles(c.conversation) != "user:hi" {
		t.Fatalf("asked with %q", roles(c.conversation))
	}
	// Between 15 and 25 seconds is the 20 seconds of titleTime, written out:
	// a test that takes its expectation from the constant would not notice a
	// change of it.
	deadline, ok := c.ctx.Deadline()
	if d := time.Until(deadline); !ok || d > 25*time.Second || d < 15*time.Second {
		t.Fatalf("no time to name it: %v", deadline)
	}
	c.reply <- titled{} // no topic yet
	until(t, live, Answer)
	s.Ask(ctx, "s1", "plan my garden")
	c = n.next(t)
	if roles(c.conversation) != "user:hi assistant:pong user:plan my garden" {
		t.Fatalf("asked with %q", roles(c.conversation))
	}
	c.reply <- titled{title: "Garden plans"}
	if got := until(t, live, Title, Answer); kinds(got) != "message title" || got[1].Text != "Garden plans" {
		t.Fatalf("events %+v", got)
	}
	until(t, live, Answer)
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Title != "Garden plans" {
		t.Fatalf("list %+v", list)
	}
	if title, err := arch.Title(history.Owner("s1")); err != nil || title != "Garden plans" {
		t.Fatalf("kept %q, %v", title, err)
	}
	// Once named, a session is not named again.
	s.Ask(ctx, "s1", "and the roses?")
	until(t, live, Answer)
	s.Close()
	if n.asked() != 2 || len(l.lines) != 0 {
		t.Fatalf("asked %d times, log %q", n.asked(), l.lines)
	}

	// Loaded again, the session has its title, and is not named again.
	arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "pong"}}, nil)
	n = newNamer()
	s, _, l = archived(t, &fakeAgent{title: n.title}, dir, time.Now())
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Title != "Garden plans" {
		t.Fatalf("archived list %+v", list)
	}
	past, live, _ := s.Watch(ctx, "s1")
	if kinds(past) != "message answer title" || past[2].Text != "Garden plans" || !past[2].At.Equal(past[1].At) {
		t.Fatalf("past %+v", past)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Title != "Garden plans" {
		t.Fatalf("loaded list %+v", list)
	}
	s.Ask(ctx, "s1", "more")
	until(t, live, Answer)
	s.Close()
	if n.asked() != 0 || len(l.lines) != 0 {
		t.Fatalf("asked %d times, log %q", n.asked(), l.lines)
	}
}

// Without a topic after some messages, the agent is not asked any more,
// whatever became of their answers.
func TestTitleGivesUp(t *testing.T) {
	n := newNamer()
	s := service(t, &fakeAgent{title: n.title, run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "fail" {
			return "", errors.New("no LLM")
		}
		return "pong", nil
	}})
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "fail")
	n.next(t).reply <- titled{}
	until(t, live, Failed)
	for range titleTries - 1 {
		s.Ask(ctx, "s1", "hi")
		n.next(t).reply <- titled{}
		until(t, live, Answer)
	}
	s.Ask(ctx, "s1", "hi")
	until(t, live, Answer)
	s.Close()
	if n.asked() != titleTries {
		t.Fatalf("asked %d times", n.asked())
	}
	if list, _ := s.List(ctx); list[0].Title != "fail" {
		t.Fatalf("list %+v", list)
	}
}

// Without an archive the title is not kept, but told; one that is too long
// is cut.
func TestTitleWithoutArchive(t *testing.T) {
	n := newNamer()
	s := service(t, &fakeAgent{title: n.title})
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "hi")
	long := strings.Repeat("ä", history.MaxTitle)
	n.next(t).reply <- titled{title: long}
	if got := until(t, live, Title); got[len(got)-1].Text != long[:history.MaxTitle] {
		t.Fatalf("events %+v", got)
	}
	until(t, live, Answer)
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Title != long[:history.MaxTitle] {
		t.Fatalf("list %+v", list)
	}
	// What fails is nothing to log without an archive.
	_, live, _ = s.Watch(ctx, "s2")
	s.Ask(ctx, "s2", "hi")
	n.next(t).reply <- titled{err: errors.New("no LLM")}
	until(t, live, Answer)
}

// What fails on the way to a title is logged, but not that the answer was
// stopped; the session goes on without a title.
func TestTitleFails(t *testing.T) {
	dir := t.TempDir()
	n := newNamer()
	s, _, l := archived(t, &fakeAgent{title: n.title}, dir, time.Now())
	s.archive.Archive = unavailableArchive(t)
	ctx := context.Background()
	past, live, _ := s.Watch(ctx, "s1")
	if len(past) != 0 || !l.has("the title of the session s1 cannot be read") {
		t.Fatalf("past %+v, log %q", past, l.lines)
	}
	s.Ask(ctx, "s1", "plan my garden")
	n.next(t).reply <- titled{err: errors.New("no LLM")}
	until(t, live, Answer)
	s.Ask(ctx, "s1", "plan my garden")
	n.next(t).reply <- titled{title: "Garden plans"}
	until(t, live, Answer)
	s.Ask(ctx, "s1", "hi")
	until(t, live, Answer)
	for _, want := range []string{"the session s1 cannot be named: no LLM", "the title of the session s1 cannot be kept"} {
		if !l.has(want) {
			t.Errorf("not logged: %q in %q", want, l.lines)
		}
	}

	// A title that is asked for while the answer is stopped is not: an
	// answer that was in already is given.
	n = newNamer()
	s, _, l = archived(t, &fakeAgent{title: n.title}, t.TempDir(), time.Now())
	_, live, _ = s.Watch(ctx, "s2")
	s.Ask(ctx, "s2", "hi")
	c := n.next(t)
	s.Cancel(ctx, "s2")
	<-c.ctx.Done()
	c.reply <- titled{err: c.ctx.Err()}
	until(t, live, Answer)
	if len(l.lines) != 0 {
		t.Errorf("log %q", l.lines)
	}
}

// A title of exactly the length that is kept is kept as it is: the message
// that carries it is no valid UTF-8, and nothing cuts it.
func TestTitleOfExactlyTheLimitIsKept(t *testing.T) {
	title := strings.Repeat("x", history.MaxTitle-1) + "\xff"
	a := &fakeAgent{title: func(context.Context, []openai.Message) (string, error) { return title, nil }}
	s := service(t, a)
	ctx := context.Background()
	_, live, err := s.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(ctx, "s1", "hi"); err != nil {
		t.Fatal(err)
	}
	got := until(t, live, Title)
	if got[len(got)-1].Text != title {
		t.Fatalf("the title is %q", got[len(got)-1].Text)
	}
}
