package assistant

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

func TestMessageSteeringIsLoggedWithSources(t *testing.T) {
	a, log := recorded(t, ownerConfig(), &scriptLLM{})
	queue := []openai.Message{
		{Role: "user", Content: "first here", Source: openai.MessageSource{Interface: "gui"}},
		{Role: "user", Content: "second here", Source: openai.MessageSource{Interface: "tui"}},
	}
	ctx := agent.WithSteering(context.Background(), func() []string {
		t.Error("legacy steering used despite message steering")
		return nil
	})
	ctx = agent.WithMessageSteering(ctx, func() []openai.Message {
		messages := queue
		queue = nil
		return messages
	})
	got, err := a.Ask(ctx, "c1", `!echo {"text":"hi"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "source interface = tui") || !strings.HasSuffix(got, "second here") {
		t.Fatalf("steering source lost: %q", got)
	}
	lines := strings.Join(log.take(), "\n")
	for _, text := range []string{"first here", "second here"} {
		if strings.Count(lines, "message from the owner on the way: "+strconv.Quote(text)) != 1 {
			t.Errorf("steering not logged exactly once: %s", lines)
		}
	}
}
