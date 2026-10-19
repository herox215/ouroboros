package session

import (
	"context"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

// The agent of the owner answers the thread as its agent: with the sessions
// of the last day before the message of the owner, and the tools of the
// thread, with which it starts a session and ends its answer without a word.
// The answer of the session is the answer the thread shows, and the agent of
// the thread knows it with its next answer.
func TestThreadWithTheAssistant(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServer(t), Model: "gpt-4o", Name: "me"}
	b := assistant.Builder{
		LLM:   func(access string) (agent.LLM, error) { return openai.New(access, openaitest.Key), nil },
		Tools: func() []agent.Tool { return nil },
	}
	s := service(t, &fakeAgent{})
	s.agent = assistant.New(func() (config.Config, error) { return cfg, nil }, b, func(string, ...any) {})
	ctx := WithMessageSource(context.Background(), MessageSource{Interface: "tui"})
	asked(t, s, "s1", "the login")
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, ThreadID, `!session_new {"text":"fix it"}`); err != nil {
		t.Fatal(err)
	}
	shownLines := shown(working(t, thread))
	if len(shownLines) < 2 || !strings.HasSuffix(shownLines[len(shownLines)-1], "last=fix it") ||
		!strings.Contains(shownLines[len(shownLines)-1], backgroundHead) {
		t.Fatalf("the completion did not pass through the thread agent: %q", shownLines)
	}
	_, err := s.Contents(ctx, ThreadID)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Ask(ctx, ThreadID, "hello"); err != nil {
		t.Fatal(err)
	}
	got := working(t, thread)
	if shown := shown(got); len(shown) != 2 || !strings.HasSuffix(shown[1], "last=hello") {
		t.Fatalf("the thread showed %q", shown)
	}
	lines, _ := s.Contents(ctx, ThreadID)
	if !slices.ContainsFunc(lines, func(l Line) bool {
		return strings.Contains(l.Text, backgroundHead) && strings.HasSuffix(l.Text, "last=fix it")
	}) {
		t.Fatalf("the agent of the thread does not know what was answered: %+v", lines)
	}
	// The list of the sessions changed with the new one, and is told anew;
	// the one before stays as it was told.
	if rosters := slices.DeleteFunc(lines, func(l Line) bool { return !isRoster(l.Text) }); len(rosters) < 2 || !strings.Contains(rosters[len(rosters)-1].Text, `"fix it"`) {
		t.Fatalf("the agent of the thread is sent the lists %+v", rosters)
	}
	list, _ := s.List(ctx)
	if len(list) != 2 || list[1].ID != "s1" {
		t.Fatalf("list %+v", list)
	}
}
