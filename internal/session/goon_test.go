package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// A session whose answer asked to go on by itself in the build it asked for
// goes on there, told so in place of a message of the owner: once.
func TestGoOn(t *testing.T) {
	if AskGoOn(context.Background()) {
		t.Fatal("outside an answer of a node somebody goes on")
	}
	dir := t.TempDir()
	var logged []string
	log := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	told := make(chan string, 4)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "build" {
			AskRestart(ctx, "/new/ouro")
			AskGoOn(ctx)
			return "built", nil
		}
		told <- text
		return "went on", nil
	}}
	inNode := func(dir string) (*Service, chan string) {
		s := service(t, a)
		s.Archive(history.Open(dir), log, func(string) string { return "" })
		replaced := make(chan string, 1)
		s.Attach(nil, func(exe string) { replaced <- exe })
		return s, replaced
	}
	s, replaced := inNode(dir)
	s.Ask(context.Background(), "s1", "build")
	if exe := <-replaced; exe != "/new/ouro" {
		t.Fatalf("replaced by %q", exe)
	}

	next, _ := inNode(dir) // the new build
	next.GoOn()
	select {
	case got := <-told:
		if got != goOnText {
			t.Fatalf("told %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not go on")
	}
	next.GoOn()
	select {
	case got := <-told:
		t.Fatalf("it went on twice: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	if len(logged) != 0 {
		t.Fatalf("log %v", logged)
	}

	// Without an archive nothing goes on, and what cannot be marked, read or
	// asked is logged.
	service(t, a).GoOn()
	s, replaced = inNode(t.TempDir())
	s.archive.Archive = unavailableArchive(t)
	s.Ask(context.Background(), "s1", "build")
	<-replaced
	logs := func(want string) {
		t.Helper()
		if !strings.Contains(strings.Join(logged, "\n"), want) {
			t.Fatalf("log %v, want %q", logged, want)
		}
		logged = nil
	}
	logs("the session s1 does not go on by itself in the new build")
	stopped, _ := inNode(dir)
	history.Open(dir).GoOn("owner/s2")
	stopped.Close()
	stopped.GoOn()
	logs("the session owner/s2 does not go on by itself")
	history.Open(dir).GoOn("owner/s3")
	next.archive.Archive = unavailableArchive(t)
	next.GoOn()
	logs("which sessions go on by themselves cannot be read")
}
