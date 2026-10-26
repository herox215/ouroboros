package gui

import (
	"context"
	"errors"
	"fmt"
	"ouroboros/internal/chat"
	"testing"
)

// What the owner writes goes the way the State of the window said: Ask
// starts an answer, Steer and an answer to a question go through Steer
// alone, as in the terminal UI. Conversations of peers are only to read.
func TestCreateAndSendThroughSessions(t *testing.T) {
	sentinel := errors.New("service unavailable")
	var calls []string
	s := &testSessions{
		newSession: func(context.Context) (string, error) { calls = append(calls, "new"); return "created", nil },
		ask: func(_ context.Context, id, text string) error {
			calls = append(calls, "ask:"+id+":"+text)
			return nil
		},
		steer: func(_ context.Context, id, text string) error {
			calls = append(calls, "steer:"+id+":"+text)
			return sentinel
		},
	}
	other := &testSessions{newSession: func(context.Context) (string, error) { return "other", nil }}
	w := sessionWindow(t, s, other)
	id, err := w.NewSession(0)
	if err != nil || id != "owner/created" {
		t.Fatalf("new: %q %v", id, err)
	}
	if err := w.Deliver(0, id, " hello\nworld ", chat.Ask); err != nil {
		t.Fatal(err)
	}
	if err := w.Deliver(0, id, "next", chat.Steer); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := w.Deliver(0, "owner/thread", "Ja", chat.Answer); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	want := "[new ask:created: hello\nworld  steer:created:next steer:thread:Ja]"
	if fmt.Sprint(calls) != want {
		t.Fatalf("calls: %q", fmt.Sprint(calls))
	}
	if id, err := w.NewSession(1); err != nil || id != "owner/other" {
		t.Fatalf("profile: %s %v", id, err)
	}
	before := len(calls)
	for _, id := range []string{"plain", "peer-key/chat", "owner/..", "owner/a/b"} {
		if err := w.Deliver(0, id, "hello", chat.Ask); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if err := w.Deliver(0, "peer-key/chat", "hello", chat.Answer); !errors.Is(err, chat.ErrReadOnly) {
		t.Fatalf("answered a peer: %v", err)
	}
	if len(calls) != before {
		t.Fatal("invalid input reached service")
	}
	for _, i := range []int{-1, 2} {
		if _, err := w.NewSession(i); err == nil {
			t.Fatal("invalid profile")
		}
		if err := w.Deliver(i, "owner/a", "hello", chat.Ask); err == nil {
			t.Fatal("invalid send profile")
		}
	}
	s.newSession = func(context.Context) (string, error) { return "", sentinel }
	if _, err := w.NewSession(0); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestWindowNodeLifecycle(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			opens, releases := 0, 0
			s := &testSessions{
				open: func(context.Context) (bool, error) { opens++; return started, nil },
				release: func(ctx context.Context) error {
					releases++
					if ctx.Err() != nil {
						t.Fatal(ctx.Err())
					}
					return nil
				},
				newSession: func(context.Context) (string, error) { return "new", nil },
			}
			w := sessionWindow(t, s)
			if _, err := w.Sessions(0); err != nil {
				t.Fatal(err)
			}
			if _, err := w.NewSession(0); err != nil {
				t.Fatal(err)
			}
			w.Close()
			w.Close()
			want := 0
			if started {
				want = 1
			}
			if opens != 1 || releases != want {
				t.Fatalf("open %d release %d", opens, releases)
			}
			if _, err := w.NewSession(0); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
	sentinel := errors.New("cannot start")
	w := sessionWindow(t, &testSessions{open: func(context.Context) (bool, error) { return false, sentinel }})
	if _, err := w.NewSession(0); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := w.Deliver(0, "owner/a", "hello", chat.Ask); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

// The last message queued comes back as the terminal UI takes it back, only
// from a session of the owner.
func TestUnsteerThroughSessions(t *testing.T) {
	sentinel := errors.New("cannot start")
	var asked []string
	w := sessionWindow(t, &testSessions{unsteer: func(_ context.Context, id string) (string, error) {
		asked = append(asked, id)
		return "later", nil
	}})
	if text, err := w.Unsteer(0, "owner/a"); err != nil || text != "later" {
		t.Fatalf("taken back %q %v", text, err)
	}
	if _, err := w.Unsteer(0, "peer-key/chat"); !errors.Is(err, chat.ErrReadOnly) {
		t.Fatalf("a peer: %v", err)
	}
	for _, id := range []string{"plain", "owner/.."} {
		if _, err := w.Unsteer(0, id); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if _, err := w.Unsteer(1, "owner/a"); err == nil {
		t.Fatal("invalid profile")
	}
	if fmt.Sprint(asked) != "[a]" {
		t.Fatalf("asked %q", asked)
	}
	broken := sessionWindow(t, &testSessions{open: func(context.Context) (bool, error) { return false, sentinel }})
	if _, err := broken.Unsteer(0, "owner/a"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
