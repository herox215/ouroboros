package session

import (
	"os"
	"os/exec"
	"testing"

	"ouroboros/internal/history"
)

func TestRestartCompletionAcrossProcesses(t *testing.T) {
	const key = "OURO_TEST_RESTART_ARCHIVE"
	if dir := os.Getenv(key); dir != "" {
		arch := history.Open(dir)
		defer arch.Close()
		s := service(t, &fakeAgent{})
		s.Archive(arch, t.Logf, func(string) string { return "" })
		events, _, err := s.Watch(t.Context(), "trigger")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, e := range events {
			if e.Kind == Restarted {
				count++
				if e.PresentationID != "process-restart" {
					t.Fatalf("completion identity: %q", e.PresentationID)
				}
			}
		}
		if count != 1 {
			t.Fatalf("completion count: %d", count)
		}
		return
	}
	dir := t.TempDir()
	arch := history.Open(dir)
	if err := arch.PendingRestart("owner/trigger", processInstance, "process-restart", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	if err := arch.Close(); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestRestartCompletionAcrossProcesses$")
	cmd.Env = append(os.Environ(), key+"="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("replacement process: %v\n%s", err, output)
	}
	reopened := history.Open(dir)
	defer reopened.Close()
	records, err := reopened.Records("owner/trigger")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Role != history.Restarted || records[0].PresentationID != "process-restart" {
		t.Fatalf("completion did not persist: %+v", records)
	}
}
