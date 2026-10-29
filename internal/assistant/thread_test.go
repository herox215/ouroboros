package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/openai"
	"ouroboros/internal/workdir"
)

// fakeThread hands on what the agent of the thread asks for, and fails with
// err if it is set.
type fakeThread struct {
	err     error
	sent    []string // as "ID TEXT"
	started []string // as "DIRECTORY TEXT"
	sought  []string // the queries
	stopped []string // as "ID JOB"
}

func (f *fakeThread) State(_ context.Context, id string) (string, error) {
	return "state " + id, f.err
}

func (f *fakeThread) Stop(_ context.Context, id string, job int) (string, error) {
	f.stopped = append(f.stopped, fmt.Sprint(id, " ", job))
	return "stopped", f.err
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
	if got, err := a.Ask(ctx, "thread", `!session_status {"session":"s1"}`, nil); result(t, got, err) != "state s1" {
		t.Fatalf("status: %s", got)
	}
	if got, err := a.Ask(ctx, "thread", `!session_stop {"session":"s1","job":2}`, nil); result(t, got, err) != "stopped" || strings.Join(thread.stopped, "|") != "s1 2" {
		t.Fatalf("stop: %s, %q", got, thread.stopped)
	}
	thread.err = errTest
	for _, call := range []string{`!session_send {"session":"s1"}`, `!session_new {}`, `!session_new [1]`, `!session_status {"session":"s1"}`, `!session_stop {"session":"s1"}`, `!session_stop [1]`, `!session_status [1]`} {
		got, err := a.Ask(ctx, "thread", call, nil)
		if got := result(t, got, err); !strings.HasPrefix(got, "error: ") {
			t.Fatalf("%s: %s", call, got)
		}
	}
	for _, tool := range threadTools("3 hours") {
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
		!strings.Contains(got, "briefly acknowledge ordinary work") || !strings.Contains(got, "only if you do not also hand that request on") ||
		!strings.Contains(got, "omit this acknowledgement") || !strings.Contains(got, "A failed handoff leaves the original request unresolved") ||
		!strings.Contains(got, "finish silently instead of announcing or confirming it in text") ||
		!strings.Contains(got, "Still report failures, necessary questions or material uncertainty") ||
		!strings.Contains(got, "it need not produce a text reply when the visible action already suffices") ||
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

// A tool of the thread that is given arguments it cannot read fails, and
// hands nothing on: the session is asked nothing, the conversation searched
// for nothing.
func TestThreadToolsRefuseMalformedArguments(t *testing.T) {
	thread := &fakeThread{}
	ctx := WithThread(context.Background(), thread)
	for tool, args := range map[string]string{
		ToolSessionSend:  `{"session":1}`,
		ToolSearchThread: `{"query":1}`,
	} {
		found := false
		for _, candidate := range threadTools("3 hours") {
			if candidate.Name != tool {
				continue
			}
			found = true
			if got, err := candidate.Run(ctx, json.RawMessage(args)); err == nil || got != "" {
				t.Errorf("%s with %s: %q, %v", tool, args, got, err)
			}
		}
		if !found {
			t.Fatalf("%s is no tool of the thread", tool)
		}
	}
	if len(thread.sent)+len(thread.started)+len(thread.sought) != 0 {
		t.Fatalf("handed on: sent %q, started %q, sought %q", thread.sent, thread.started, thread.sought)
	}
}

// silentLLM answers nothing at all, however often it is asked: what a thread
// gets for a session that answered nothing and said it went well.
type silentLLM struct{}

func (silentLLM) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	return openai.Message{}, nil
}

// An answer of the thread that answers nothing is logged as one that handed
// on, also when nothing went wrong: what the log tells is what a review of
// the conversation reads. No answer of the thread is one a message follows up
// on, so nothing is kept for the next message.
func TestThreadLogsAnAnswerThatAnsweredNothing(t *testing.T) {
	a, log := recorded(t, ownerConfig(), silentLLM{})
	log.take()
	ctx := WithThread(context.Background(), &fakeThread{})
	reply, err := a.Run(ctx, "chat", "hi", nil, nil)
	if err != nil || reply != "" {
		t.Fatalf("got %q, %v", reply, err)
	}
	lines := log.take()
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "chat chat: answered after ") || !strings.Contains(last, handedOn) {
		t.Fatalf("the log of an answer that answered nothing: %q", lines)
	}
	if _, _, ok := a.followed("chat", "and?", time.Now()); ok {
		t.Fatal("an answer of the thread is followed up on")
	}
	// Outside the thread an answer that says nothing is an answer that says
	// nothing: nothing was handed on.
	if reply, err := a.Run(context.Background(), "other", "hi", nil, nil); err != nil || reply != "" {
		t.Fatalf("outside the thread: %q, %v", reply, err)
	}
	if lines := log.take(); !strings.HasSuffix(lines[len(lines)-1], `: ""`) {
		t.Fatalf("the log outside the thread: %q", lines)
	}
}

// session_new returns the ID of the session it started, as its description
// promises, as the thread is told it: so the agent of the thread and the
// front ends can tell which session a part of the thread started.
func TestSessionNewNamesItsSession(t *testing.T) {
	cfg, b := directNode(t)
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	defer a.Stop()
	ctx := WithThread(context.Background(), &fakeThread{})
	got, err := a.Ask(ctx, "thread", `!session_new {"text":"go"}`, nil)
	if got := result(t, got, err); got != "Work started in the background in session s2." {
		t.Fatalf("session_new: %s", got)
	}
}
