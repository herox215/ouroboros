package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/workdir"
)

// fakeThread hands on what the agent of the thread asks for, and fails with
// err if it is set.
type fakeThread struct {
	err     error
	sent    []string // as "ID TEXT"
	started []string // as "DIRECTORY TEXT"
}

func (f *fakeThread) Send(_ context.Context, id, text string) (string, string, error) {
	f.sent = append(f.sent, id+" "+text)
	return "Fix", "the session answers it now", f.err
}

func (f *fakeThread) Start(_ context.Context, text, directory string) (string, error) {
	f.started = append(f.started, directory+" "+text)
	return "s2", f.err
}

// In the thread the owner's agent hands messages on, and ends its answer
// with that, without a word: the session answers. It has the tools of the
// thread alone.
func TestThreadHandsOn(t *testing.T) {
	cfg, b := directNode(t)
	var logged []string
	var mu sync.Mutex
	a := New(func() (config.Config, error) { return cfg, nil }, b, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	thread := &fakeThread{}
	ctx := WithThread(context.Background(), thread)
	for _, call := range []string{`!session_send {"session":"s1","text":"more"}`, `!session_new {"text":"go","directory":"/p"}`, `!session_send {"session":"s1"}`, `!session_new {}`} {
		if got, err := a.Ask(ctx, "thread", call, nil); err != nil || got != "" {
			t.Fatalf("%s: %q, %v", call, got, err)
		}
	}
	if strings.Join(thread.sent, "|") != "s1 more|s1 " || strings.Join(thread.started, "|") != "/p go| " {
		t.Fatalf("sent %q, started %q", thread.sent, thread.started)
	}
	// Its answers are not reviewed: that it says nothing is how it hands on.
	reviewed := WithReview(ctx, func(string) { t.Error("a review of the thread") })
	if got, err := a.Ask(reviewed, "thread", `!session_send {"session":"s1","text":"more"}`, nil); err != nil || got != "" {
		t.Fatalf("reviewed: %q, %v", got, err)
	}
	a.reviewing.Lock()
	_, kept := a.last["thread"]
	a.reviewing.Unlock()
	if kept {
		t.Fatal("the answer of the thread is kept for a review")
	}
	a.Stop() // the log is written
	mu.Lock()
	log := strings.Join(logged, "\n")
	mu.Unlock()
	if strings.Contains(log, "signals:") || !strings.Contains(log, `"[nothing: the thread hands on, and the session answers]"`) {
		t.Fatalf("log:\n%s", log)
	}
	contents, _ := a.ContentsContext(ctx, "thread")
	if results := contents.Messages[2].Content + "|" + contents.Messages[5].Content; results != `Handed to "Fix": the session answers it now.|Started a session with it: it answers it now.` {
		t.Fatalf("what the agent of the thread was told: %s", results)
	}

	// What the thread cannot hand on is the result of the call, and so is
	// a call it cannot read.
	thread.err = errTest
	for _, call := range []string{`!session_send {"session":"s1","text":"more"}`, `!session_new {"text":"go"}`, `!session_new [1]`, `!session_send [1]`} {
		got, err := a.Ask(ctx, "thread", call, nil)
		if got := result(t, got, err); !strings.HasPrefix(got, "error: ") {
			t.Fatalf("%s: %s", call, got)
		}
	}

	// The tools of the thread are of the thread alone, and the other tools
	// of the owner's agent are not of the thread.
	contents, err := a.ContentsContext(ctx, "thread")
	if err != nil || len(contents.Tools) != 2 || contents.Tools[0].Function.Name != ToolSessionSend || contents.Tools[1].Function.Name != ToolSessionNew {
		t.Fatalf("the tools of the thread: %+v, %v", contents.Tools, err)
	}
	contents, _ = a.ContentsContext(context.Background(), "c")
	for _, tool := range contents.Tools {
		if strings.HasPrefix(tool.Function.Name, "session_") {
			t.Fatalf("the owner's agent has %s", tool.Function.Name)
		}
	}
	for _, tool := range threadTools() {
		if _, err := tool.Run(context.Background(), json.RawMessage(`{}`)); err == nil || err.Error() != tool.Name+" is for the thread alone" {
			t.Fatalf("%s outside the thread: %v", tool.Name, err)
		}
	}
}

// The agent of the thread is told what it is, and where the sessions it
// starts work.
func TestThreadPrompt(t *testing.T) {
	cfg, b := directNode(t)
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	ctx := WithThread(context.Background(), &fakeThread{})
	got, err := a.Ask(ctx, "thread", "?system", nil)
	if err != nil || !strings.HasPrefix(got, `You are "me", the personal agent of the owner of this Ouroboros node, and this is the one conversation`) ||
		!strings.Contains(got, "The owner is never to notice the sessions") || !strings.Contains(got, "goes to it word for word") ||
		!strings.Contains(got, "where session_new starts a session that names no directory, is your workspace. ") ||
		!strings.Contains(got, ownerStyle) || strings.Contains(got, workHint) {
		t.Fatalf("the prompt of the thread: %q, %v", got, err)
	}
	got, err = a.Ask(workdir.With(ctx, "/home/me/app"), "thread", "?system", nil)
	if err != nil || !strings.Contains(got, `starts a session that names no directory, is "/home/me/app". `) {
		t.Fatalf("with a project: %q, %v", got, err)
	}
	if got, err := a.Ask(context.Background(), "c", "?system", nil); err != nil || strings.Contains(got, "the one conversation the owner has with you") {
		t.Fatalf("the owner's agent: %q, %v", got, err)
	}
}

// What the owner's agent is told without being asked comes before the next
// message of its conversation. An agent that is not set up is told nothing.
func TestNote(t *testing.T) {
	cfg, b := directNode(t)
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	if err := a.Note(context.Background(), "thread", "[the session answered]"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Ask(context.Background(), "thread", "hi", nil); err != nil || got != "[gpt-4o] turns=2 last=hi" {
		t.Fatalf("got %q, %v", got, err)
	}
	unset := New(func() (config.Config, error) { return config.Config{}, nil }, b, discard)
	if err := unset.Note(context.Background(), "thread", "x"); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("not set up: %v", err)
	}
}
