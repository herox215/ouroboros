package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/internal/workdir"
)

// canonicalTempDir matches the canonical paths returned by directory resolution,
// including when the system temporary directory is reached through a symlink.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func directoryAt(t *testing.T, api API, id string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	past, _, err := api.Watch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range past {
		if e.Kind == Directory {
			return e.Text
		}
	}
	t.Fatalf("no directory event for %s: %+v", id, past)
	return ""
}

func TestDirectoryIsolationAndValidation(t *testing.T) {
	root := canonicalTempDir(t)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	took := make(chan string, 8)
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		took <- workdir.Of(ctx)
		return "pong", nil
	}})
	ctx := context.Background()
	if err := s.SetInitialDirectory(root); err != nil {
		t.Fatal(err)
	}
	id, err := s.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, id); got != root {
		t.Fatalf("new: %q", got)
	}
	if err := s.SetDirectory(ctx, "s1", "sub"); err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, "s1"); got != sub {
		t.Fatalf("relative: %q", got)
	}
	if err := s.SetDirectory(ctx, "s1", ".."); err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, "s1"); got != root {
		t.Fatalf("relative to project: %q", got)
	}
	if err := s.SetDirectory(ctx, "s1", sub); err != nil {
		t.Fatal(err)
	}
	asked(t, s, "s1", "one")
	asked(t, s, "s2", "two")
	if one, two := <-took, <-took; one != sub || two != root {
		t.Fatalf("directories %q, %q", one, two)
	}
	if err := s.SetDirectory(ctx, "s1", ""); err != nil {
		t.Fatal(err)
	}
	asked(t, s, "s1", "reset")
	if got := <-took; got != "" {
		t.Fatalf("reset %q", got)
	}
	if err := s.SetDirectory(ctx, "s1", "sub"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing"), "~someone"} {
		if err := s.SetDirectory(ctx, "s1", path); err == nil {
			t.Fatalf("accepted %q", path)
		}
		if got := directoryAt(t, s, "s1"); got != sub {
			t.Fatalf("invalid path changed project: %q", got)
		}
	}
	t.Setenv("HOME", root)
	if err := s.SetDirectory(ctx, "s1", "~/sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDirectory(ctx, "s1", "~"); err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, "s1"); got != root {
		t.Fatalf("home %q", got)
	}
	if err := s.SetDirectory(ctx, "../bad", root); !errors.Is(err, ErrNoSession) {
		t.Fatalf("id: %v", err)
	}
	s.Close()
	if err := s.SetDirectory(ctx, "s1", root); !errors.Is(err, ErrStopped) {
		t.Fatalf("closed: %v", err)
	}
}

func TestDirectoryAbsoluteFallback(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	s := service(t, &fakeAgent{})
	if err := s.SetDirectory(context.Background(), "s1", "."); err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, "s1"); got != cwd {
		t.Fatalf("fallback %q, want %q", got, cwd)
	}
}

func TestDirectoryPersistence(t *testing.T) {
	dir, project, initial := t.TempDir(), canonicalTempDir(t), canonicalTempDir(t)
	ctx := context.Background()
	s, arch, _ := archived(t, &fakeAgent{}, dir, time.Now())
	for id, path := range map[string]string{"saved": project, "empty": "", "quiet": project} {
		if err := s.SetDirectory(ctx, id, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetInitialDirectory(initial); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	arch.Append(history.Who{Session: "saved"}, []openai.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "pong"}}, nil)
	s.Close()
	s, _, _ = archived(t, &fakeAgent{}, dir, time.Now())
	if err := s.SetInitialDirectory(initial); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"saved": project, "empty": "", "quiet": project, fresh: initial} {
		if got := directoryAt(t, s, id); got != want {
			t.Fatalf("%s: %q, want %q", id, got, want)
		}
	}
}

func TestDirectoryBusyAndTaskContext(t *testing.T) {
	project := canonicalTempDir(t)
	entered := make(chan string, 1)
	gate := make(chan struct{})
	taskDirectory := make(chan string, 1)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "wait" {
			entered <- workdir.Of(ctx)
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if text == "delegate" {
			_, err := assistant.TasksOf(ctx).Start([]assistant.Task{{Title: "wait", Run: func(ctx context.Context) (string, error) {
				taskDirectory <- workdir.Of(ctx)
				<-ctx.Done()
				return "", ctx.Err()
			}}})
			return "started", err
		}
		return "pong", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	if err := s.SetDirectory(ctx, "s1", project); err != nil {
		t.Fatal(err)
	}
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "wait"); err != nil {
		t.Fatal(err)
	}
	if got := <-entered; got != project {
		t.Fatalf("answer directory %q", got)
	}
	if err := s.SetDirectory(ctx, "s1", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("answer busy: %v", err)
	}
	close(gate)
	until(t, live, Answer)
	asked(t, s, "s1", "delegate")
	if got := <-taskDirectory; got != project {
		t.Fatalf("task directory %q", got)
	}
	if err := s.SetDirectory(ctx, "s1", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("task busy: %v", err)
	}
}

type directoryContentsAgent struct{ *fakeAgent }

func (a directoryContentsAgent) ContentsContext(ctx context.Context, _ string) (assistant.Contents, error) {
	return assistant.Contents{System: workdir.Of(ctx)}, nil
}

func TestDirectoryContentsContext(t *testing.T) {
	s := New(directoryContentsAgent{&fakeAgent{}})
	t.Cleanup(s.Close)
	project := canonicalTempDir(t)
	ctx := context.Background()
	if err := s.SetDirectory(ctx, "s1", project); err != nil {
		t.Fatal(err)
	}
	lines, err := s.Contents(ctx, "s1")
	if err != nil || len(lines) != 1 || lines[0].Text != project {
		t.Fatalf("contents %+v, %v", lines, err)
	}
}

func TestClientDirectory(t *testing.T) {
	s := service(t, &fakeAgent{})
	c := NewClient(serve(t, Handler(s)).dir)
	ctx := context.Background()
	project := canonicalTempDir(t)
	_, live, err := c.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDirectory(ctx, "s1", project); err != nil {
		t.Fatal(err)
	}
	if got := until(t, live, Directory); got[0].Text != project {
		t.Fatalf("directory %+v", got)
	}
	if got := directoryAt(t, c, "s1"); got != project {
		t.Fatalf("replayed %q", got)
	}
	if err := c.SetDirectory(ctx, "s1", filepath.Join(project, "missing")); err == nil {
		t.Fatal("accepted missing directory")
	}
	if err := c.SetDirectory(ctx, "../bad", project); !errors.Is(err, ErrNoSession) {
		t.Fatalf("id: %v", err)
	}
	s.mu.Lock()
	se := s.get("s1")
	se.cancel = func() {}
	s.mu.Unlock()
	if err := c.SetDirectory(ctx, "s1", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	s.mu.Lock()
	se.cancel = nil
	s.mu.Unlock()
	if err := c.SetDirectory(ctx, "s1", ""); err != nil {
		t.Fatal(err)
	}
	if got := until(t, live, Directory); got[0].Text != "" {
		t.Fatalf("reset %+v", got)
	}
}
