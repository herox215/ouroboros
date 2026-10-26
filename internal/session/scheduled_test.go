package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/workdir"
)

func scheduledResult(t *testing.T, result <-chan ScheduledResult) ScheduledResult {
	t.Helper()
	select {
	case out, ok := <-result:
		if !ok {
			t.Fatal("missing result")
		}
		if _, ok := <-result; ok {
			t.Fatal("more than one result")
		}
		return out
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled result did not complete")
	}
	return ScheduledResult{}
}

func TestRunScheduledGeneratedRouted(t *testing.T) {
	directory := canonicalTempDir(t)
	reports := make(chan string, 1)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasPrefix(text, backgroundHead) {
			reports <- text
			return "relayed", nil
		}
		if workdir.Of(ctx) != directory {
			t.Errorf("directory: %q", workdir.Of(ctx))
		}
		if !strings.Contains(text, "not fresh owner input") || !strings.HasSuffix(text, "stored task") {
			t.Errorf("input: %s", text)
		}
		return "job output", nil
	}}
	s := service(t, a)
	id, result, err := s.RunScheduled(WithMessageSource(t.Context(), MessageSource{Interface: "gui"}), "daily", "stored task", directory, false)
	if err != nil {
		t.Fatal(err)
	}
	if id == ThreadID || id == "" {
		t.Fatalf("id: %q", id)
	}
	out := scheduledResult(t, result)
	if out.Text != "job output" || out.Err != nil {
		t.Fatalf("result: %+v", out)
	}
	past, _, err := s.Watch(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range past {
		if e.Kind == Message {
			found = true
			if !e.Routed || e.Source != (MessageSource{}) {
				t.Fatalf("not generated/routed: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("no input event")
	}
	select {
	case text := <-reports:
		if !strings.Contains(text, "job output") || !strings.Contains(text, `"daily"`) {
			t.Fatal(text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no thread report")
	}
}

func TestRunScheduledQuestions(t *testing.T) {
	for _, ask := range []bool{false, true} {
		t.Run(fmt.Sprint(ask), func(t *testing.T) {
			a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
				if strings.HasPrefix(text, backgroundHead) {
					return "reported", nil
				}
				if agent.CanAskOwner(ctx) != ask {
					t.Errorf("CanAskOwner = %v", agent.CanAskOwner(ctx))
				}
				return agent.AskOwner(ctx, json.RawMessage(`{"question":"Allow operation?","options":["Run it this time","Don't run it"]}`))
			}}
			s := service(t, a)
			_, live, _ := s.Watch(t.Context(), ThreadID)
			id, result, err := s.RunScheduled(t.Context(), "question", "go", "", ask)
			if err != nil {
				t.Fatal(err)
			}
			if ask {
				until(t, live, Asked)
				if err := s.Steer(t.Context(), ThreadID, "Don't run it"); err != nil {
					t.Fatal(err)
				}
			}
			out := scheduledResult(t, result)
			if ask {
				if out.Err != nil || out.Text != "Don't run it" {
					t.Fatalf("result: %+v", out)
				}
			} else {
				if !errors.Is(out.Err, agent.ErrNoOwner) {
					t.Fatalf("result: %+v", out)
				}
				past, live, _ := s.Watch(t.Context(), id)
				for _, e := range past {
					if e.Kind == Asked {
						t.Fatalf("unattended question emitted: %+v", e)
					}
				}
				// Later answers in the unattended session must not silently
				// regain permission to ask, including generated continuations.
				if err := s.askInput(t.Context(), id, "continuation", true); err != nil {
					t.Fatal(err)
				}
				for _, e := range until(t, live, Failed) {
					if e.Kind == Asked {
						t.Fatal("continuation asked the owner")
					}
				}
			}
		})
	}
}

func TestRunScheduledCancellation(t *testing.T) {
	for _, socket := range []bool{false, true} {
		t.Run(fmt.Sprint(socket), func(t *testing.T) {
			entered := make(chan struct{})
			stopped := make(chan struct{})
			s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
				close(entered)
				<-ctx.Done()
				close(stopped)
				return "", ctx.Err()
			}})
			var api API = s
			if socket {
				api = NewClient(serve(t, Handler(s)).dir)
			}
			ctx, cancel := context.WithCancel(t.Context())
			_, result, err := api.RunScheduled(ctx, "cancel", "wait", "", false)
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			cancel()
			if out := scheduledResult(t, result); !errors.Is(out.Err, context.Canceled) {
				t.Fatalf("result: %+v", out)
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("server run not canceled")
			}
		})
	}
}

func TestRunScheduledConcurrentCompletion(t *testing.T) {
	s := service(t, &fakeAgent{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, result, err := s.RunScheduled(t.Context(), "fast", "go", "", false)
			if err != nil {
				t.Error(err)
				return
			}
			if out := scheduledResult(t, result); out.Err != nil || out.Text != "pong" {
				t.Errorf("result: %+v", out)
			}
		}()
	}
	wg.Wait()
}

func TestRunScheduledReadiness(t *testing.T) {
	notReady := errors.New("not ready")
	s := service(t, &fakeAgent{ready: notReady})
	if _, _, err := s.RunScheduled(t.Context(), "job", "go", "", false); !errors.Is(err, notReady) {
		t.Fatal(err)
	}
	if len(s.sessions) != 0 {
		t.Fatal("published unready session")
	}
}

func TestRunScheduledSocketResult(t *testing.T) {
	s := service(t, &fakeAgent{})
	c := NewClient(serve(t, Handler(s)).dir)
	_, result, err := c.RunScheduled(t.Context(), "socket", "go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); out.Text != "pong" || out.Err != nil {
		t.Fatalf("result: %+v", out)
	}
}
