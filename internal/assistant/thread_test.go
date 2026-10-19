package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	sought  []string // the queries
}

func (f *fakeThread) Send(_ context.Context, id, text string) (string, string, error) {
	f.sent = append(f.sent, id+" "+text)
	return "Fix", "the session answers it now", f.err
}

func (f *fakeThread) Search(_ context.Context, query string) (string, error) {
	f.sought = append(f.sought, query)
	return "found " + query, f.err
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
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	defer a.Stop()
	thread := &fakeThread{}
	ctx := WithThread(context.Background(), thread)
	for _, call := range []string{`!session_send {"session":"s1","text":"more"}`, `!session_new {"text":"go","directory":"/p"}`} {
		got, err := a.Ask(ctx, "thread", call, nil)
		if err != nil || got == "" {
			t.Fatalf("handoff must allow an acknowledgement: %q, %v", got, err)
		}
	}
	if strings.Join(thread.sent, "|") != "s1 more" || strings.Join(thread.started, "|") != "/p go" {
		t.Fatalf("sent %q, started %q", thread.sent, thread.started)
	}
	got, err := a.Ask(ctx, "thread", `!search_thread {"query":"dialog"}`, nil)
	if got := result(t, got, err); got != "found dialog" {
		t.Fatalf("search: %s", got)
	}
	thread.err = errTest
	for _, call := range []string{`!session_send {"session":"s1"}`, `!session_new {}`, `!session_new [1]`} {
		got, err := a.Ask(ctx, "thread", call, nil)
		if got := result(t, got, err); !strings.HasPrefix(got, "error: ") {
			t.Fatalf("%s: %s", call, got)
		}
	}
	for _, tool := range threadTools() {
		if _, err := tool.Run(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Fatalf("%s ran outside the thread", tool.Name)
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
		!strings.Contains(got, "You are shown only the last 6 hours of this conversation: when the owner refers to something said before that, find it with search_thread. ") ||
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
