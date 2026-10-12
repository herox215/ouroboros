package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// An answer to a peer keeps the node as one of the owner does: a build it
// asks for replaces the node once no answer is on its way.
func TestPeerAnswers(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx := context.Background()
	asks := func(ctx context.Context) (string, error) { return fmt.Sprint(AskRestart(ctx, "/new/ouro")), nil }
	if reply, err := s.Peer(ctx, "c", asks); err != nil || reply != "false" {
		t.Fatalf("without a node that starts anew: %q, %v", reply, err)
	}

	replaced := make(chan string, 2)
	s.Attach(nil, func(exe string) { replaced <- exe })
	started, gate, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		s.Peer(ctx, "c", func(context.Context) (string, error) { close(started); <-gate; return "", nil })
		close(done)
	}()
	<-started
	failed := errors.New("failed")
	if reply, err := s.Peer(ctx, "c", func(ctx context.Context) (string, error) { r, _ := asks(ctx); return r, failed }); reply != "true" || err != failed {
		t.Fatalf("in a node that starts anew: %q, %v", reply, err)
	}
	if len(replaced) != 0 {
		t.Fatal("the answer on its way keeps the node")
	}
	close(gate)
	<-done
	if exe := <-replaced; exe != "/new/ouro" {
		t.Fatalf("replaced by %q", exe)
	}

	s.Close()
	if _, err := s.Peer(ctx, "c", asks); !errors.Is(err, ErrStopped) {
		t.Fatalf("a node that stops: %v", err)
	}
}

// What the agent said with a peer is recalled from the archive, as it would
// remember it.
func TestRecall(t *testing.T) {
	s := service(t, &fakeAgent{})
	if got := s.Recall("key", "c"); got != nil {
		t.Fatalf("without an archive: %+v", got)
	}
	dir := t.TempDir()
	a := history.Open(dir)
	var logged []string
	s.Archive(a, func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }, func(string) string { return "" })
	said := []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho"}}
	if err := a.Append(history.Who{Session: "c", Peer: "key"}, said, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Recall("key", "c"); len(got) != 2 || got[0].Content != "hi" || got[1].Content != "ho" {
		t.Fatalf("recalled %+v", got)
	}
	if got := s.Recall("other", "c"); got != nil || len(logged) != 0 {
		t.Fatalf("of another peer: %+v, log %v", got, logged)
	}
	path := filepath.Join(dir, history.Party("key"), "c.jsonl")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if got := s.Recall("key", "c"); got != nil || len(logged) != 1 {
		t.Fatalf("unreadable: %+v, log %v", got, logged)
	}
}
