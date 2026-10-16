package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"ouroboros/plugin"
)

func TestCreateAndSendThroughSessions(t *testing.T) {
	sentinel := errors.New("service unavailable")
	var calls []string
	busy := false
	s := &testSessions{
		newSession: func(context.Context) (string, error) { calls = append(calls, "new"); return "created", nil },
		ask: func(_ context.Context, id, text string) error {
			calls = append(calls, "ask:"+id+":"+text)
			if busy {
				return fmt.Errorf("running: %w", plugin.ErrBusy)
			}
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
	if err := w.SendMessage(0, id, " hello\nworld "); err != nil {
		t.Fatal(err)
	}
	busy = true
	if err := w.SendMessage(0, id, "next"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	want := "[new ask:created: hello\nworld  ask:created:next steer:created:next]"
	if fmt.Sprint(calls) != want {
		t.Fatalf("calls: %q", fmt.Sprint(calls))
	}
	if id, err := w.NewSession(1); err != nil || id != "owner/other" {
		t.Fatalf("profile: %s %v", id, err)
	}
	before := len(calls)
	for _, id := range []string{"plain", "peer-key/chat", "owner/..", "owner/a/b"} {
		if err := w.SendMessage(0, id, "hello"); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	if err := w.SendMessage(0, "owner/created", " \n\t "); err == nil {
		t.Fatal("empty message accepted")
	}
	if len(calls) != before {
		t.Fatal("invalid input reached service")
	}
	for _, i := range []int{-1, 2} {
		if _, err := w.NewSession(i); err == nil {
			t.Fatal("invalid profile")
		}
		if err := w.SendMessage(i, "owner/a", "hello"); err == nil {
			t.Fatal("invalid send profile")
		}
	}
	s.newSession = func(context.Context) (string, error) { return "", sentinel }
	if _, err := w.NewSession(0); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	s.ask = func(context.Context, string, string) error { return sentinel }
	if err := w.SendMessage(0, "owner/created", "hello"); !errors.Is(err, sentinel) {
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
	if err := w.SendMessage(0, "owner/a", "hello"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestQuestionProjection(t *testing.T) {
	p := &Profile{}
	event := plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Which database?","options":["SQLite","Postgres"],"recommended":"SQLite"}`)}
	got := p.event(event)
	if got.Question == nil || got.Question.Text != "Which database?" || len(got.Question.Options) != 2 || got.Question.Recommended != "SQLite" {
		t.Fatalf("question: %+v", got)
	}
	event.Done = true
	if p.event(event).Question != nil {
		t.Fatal("completed question has choices")
	}
}
