package session

import (
	"context"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

func TestRestartIntentPrecedesSchedulingAndSameProcessDoesNotComplete(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	defer arch.Close()
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		return "built", nil
	}})
	s.Archive(arch, t.Logf, func(string) string { return "" })
	scheduled := make(chan error, 1)
	s.Attach(nil, func(string) {
		// Reopen the archive at the scheduling boundary. The intent must not
		// complete in this image; a different image consumes it below.
		probe := history.Open(dir)
		defer probe.Close()
		got, err := probe.CompleteRestarts(processInstance)
		if err == nil && len(got) != 0 {
			t.Error("same process completed at scheduling")
		}
		scheduled <- err
	})
	if err := s.Ask(context.Background(), "trigger", "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-scheduled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart not scheduled")
	}
	reopened := service(t, &fakeAgent{})
	reopenedArch := history.Open(dir)
	defer reopenedArch.Close()
	reopened.Archive(reopenedArch, t.Logf, func(string) string { return "" })
	events, _, err := reopened.Watch(context.Background(), "trigger")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == Restarted {
			t.Fatal("same executable instance announced completion")
		}
	}
	completed, err := arch.CompleteRestarts("replacement-image")
	if err != nil || len(completed) != 1 || completed["owner/trigger"].PresentationID == "" {
		t.Fatalf("intent absent before scheduling: %v, %v", completed, err)
	}
}

func TestRestartedReplayIsScopedStableAndNotModelInput(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	defer arch.Close()
	if err := arch.PendingRestart("owner/trigger", "previous-image", "stable-restart-id", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s := service(t, &fakeAgent{})
		s.Archive(arch, t.Logf, func(string) string { return "" })
		events, _, err := s.Watch(context.Background(), "trigger")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, e := range events {
			if e.Kind == Restarted {
				count++
				if e.PresentationID != "stable-restart-id" || e.Text != "/new/ouro" {
					t.Fatalf("completion: %+v", e)
				}
			}
		}
		if count != 1 {
			t.Fatalf("restart events: %+v", events)
		}
		other, _, err := s.Watch(context.Background(), "other")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range other {
			if e.Kind == Restarted {
				t.Fatal("completion leaked to another session")
			}
		}
	}
	records, err := arch.Records("owner/trigger")
	if err != nil {
		t.Fatal(err)
	}
	_, messages := replay(records)
	if len(messages) != 0 {
		t.Fatalf("completion became model input: %+v", messages)
	}
}

func TestRestartPersistenceFailureDoesNotSchedule(t *testing.T) {
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		return "built", nil
	}})
	s.Archive(unavailableArchive(t), t.Logf, func(string) string { return "" })
	scheduled := make(chan string, 1)
	s.Attach(nil, func(exe string) { scheduled <- exe })
	if err := s.Ask(context.Background(), "trigger", "build"); err != nil {
		t.Fatal(err)
	}
	s.answers.Wait()
	select {
	case exe := <-scheduled:
		t.Fatalf("scheduled without durability: %s", exe)
	default:
	}
	events, _, err := s.Watch(t.Context(), "trigger")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == Restart {
			t.Fatal("announced a restart that was refused")
		}
	}
}
