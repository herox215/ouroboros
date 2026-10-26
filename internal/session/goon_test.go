package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/internal/storage"
)

// A session whose answer asked to go on by itself in the build it asked for
// goes on there, told so in place of a message of the owner: once.
func TestGoOn(t *testing.T) {
	if AskGoOn(context.Background()) {
		t.Fatal("outside an answer of a node somebody goes on")
	}
	dir := t.TempDir()
	// The chat was asked something before it asked for the build: the note it
	// goes on with names the chat and the task it was asked first.
	seed := history.Open(dir)
	if err := seed.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "fix issue 70"}}, nil); err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	var logged []string
	log := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	told := make(chan string, 4)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasSuffix(text, "build issue 70") {
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
	s.Ask(context.Background(), "s1", "build issue 70")
	if exe := <-replaced; exe != "/new/ouro" {
		t.Fatalf("replaced by %q", exe)
	}
	s.answers.Wait()

	next, _ := inNode(dir) // the new build
	next.GoOn()
	select {
	case got := <-told:
		if !strings.Contains(got, goOnText) {
			t.Fatalf("told %q", got)
		}
		for _, want := range []string{`This chat is "s1" in the history archive`, "fix issue 70"} {
			if !strings.Contains(got, want) {
				t.Errorf("told %q, want %q", got, want)
			}
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
	s.Ask(context.Background(), "s1", "build issue 70")
	s.answers.Wait() // restart is refused when its pending marker cannot be kept
	select {
	case exe := <-replaced:
		t.Fatalf("restart without a durable marker: %s", exe)
	default:
	}
	logs := func(want string) {
		t.Helper()
		if !strings.Contains(strings.Join(logged, "\n"), want) {
			t.Fatalf("log %v, want %q", logged, want)
		}
		logged = nil
	}
	logs("the restart of the session s1 cannot be kept")
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

// A session whose answer asked to go on by itself keeps the restart, but
// what cannot mark it to go on is logged: only that part is lost.
func TestGoOnMarkCannotBeKept(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	// Prepare the database the archive keeps open: the mark of another
	// session opens it, so the column of the going-on mark can be taken from
	// under it without being given back.
	if err := arch.Append(history.Who{Session: "s0"}, []openai.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE history_sessions RENAME COLUMN goon TO gone"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		AskGoOn(ctx)
		return "built", nil
	}}
	s := service(t, a)
	l := &logs{}
	s.Archive(arch, l.log, func(string) string { return "" })
	replaced := make(chan string, 1)
	s.Attach(nil, func(exe string) { replaced <- exe })
	if err := s.Ask(context.Background(), "s1", "build"); err != nil {
		t.Fatal(err)
	}
	s.answers.Wait()
	select {
	case exe := <-replaced:
		if exe != "/new/ouro" {
			t.Fatalf("replaced by %q", exe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the restart was not kept")
	}
	if !l.has("the session s1 does not go on by itself in the new build") {
		t.Errorf("log %q", l.lines)
	}
	if l.has("the restart of the session s1 cannot be kept") {
		t.Errorf("log %q", l.lines)
	}
}

// A session whose answer asked for a build, but not to go on by itself,
// waits in the new build for the owner.
func TestRestartWithoutGoingOn(t *testing.T) {
	dir := t.TempDir()
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		return "built", nil
	}})
	s.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	replaced := make(chan string, 1)
	s.Attach(nil, func(exe string) { replaced <- exe })
	if err := s.Ask(context.Background(), "s1", "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("the node was not replaced")
	}
	a := history.Open(dir)
	defer a.Close()
	if ids, err := a.GoingOn(); err != nil || len(ids) != 0 {
		t.Fatalf("going on: %v, %v", ids, err)
	}
}
