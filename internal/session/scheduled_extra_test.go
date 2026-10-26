package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
)

func TestRunScheduledDeadline(t *testing.T) {
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		<-ctx.Done()
		return "partial", ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, result, err := s.RunScheduled(ctx, "deadline", "wait", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); !errors.Is(out.Err, context.DeadlineExceeded) || out.Text != "partial" {
		t.Fatalf("result: %+v", out)
	}
}

func TestRunScheduledFailureRelayed(t *testing.T) {
	failure := errors.New("job failed")
	reported := make(chan string, 1)
	s := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasPrefix(text, backgroundHead) {
			reported <- text
			return "reported", nil
		}
		return "partial", failure
	}})
	_, result, err := s.RunScheduled(t.Context(), "failure", "go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); !errors.Is(out.Err, failure) || out.Text != "partial" {
		t.Fatalf("result: %+v", out)
	}
	select {
	case text := <-reported:
		if !strings.Contains(text, "failed") || !strings.Contains(text, "job failed") {
			t.Fatal(text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure not relayed")
	}
}

func TestRunScheduledRejectsBeforeStarting(t *testing.T) {
	for _, test := range []string{"canceled", "empty", "directory", "stopped", "restarting"} {
		t.Run(test, func(t *testing.T) {
			s := service(t, &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
				t.Error("unexpected run")
				return "", nil
			}})
			ctx := t.Context()
			text, directory := "go", ""
			var want error
			switch test {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "empty":
				text = "  "
				want = ErrEmpty
			case "directory":
				directory = t.TempDir() + "/missing"
			case "stopped":
				s.Close()
				want = ErrStopped
			case "restarting":
				s.node.successor = "new build"
				want = ErrRestarting
			}
			id, result, err := s.RunScheduled(ctx, "job", text, directory, false)
			if err == nil || want != nil && !errors.Is(err, want) || id != "" || result != nil {
				t.Fatalf("%q %v %v", id, result, err)
			}
			if len(s.sessions) != 0 {
				t.Fatal("rejected job published a session")
			}
		})
	}
}
