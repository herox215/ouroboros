package assistant

import (
	"context"
	"slices"
	"testing"

	"ouroboros/internal/agent"
)

// A limited answer gets only the tools named, with delegate those of its
// tasks; an unlimited one gets all of them.
func TestOnlyTools(t *testing.T) {
	quiet := func(context.Context, struct{}) (string, error) { return "", nil }
	var tools []agent.Tool
	for _, name := range []string{"memory_read", "bash", ToolDelegate, ToolTaskStop, ToolTaskTell} {
		tools = append(tools, agent.NewTool(name, "", nil, quiet))
	}
	bot := agent.New(promptLLM{}, "m", "", tools...)
	if got := toolNames(only(bot, OnlyTools(context.Background()))); len(got) != len(tools) {
		t.Fatalf("unlimited: %v", got)
	}
	ctx := WithOnlyTools(context.Background(), []string{"memory_read", "gone"})
	if got := toolNames(only(bot, OnlyTools(ctx))); !slices.Equal(got, []string{"memory_read"}) {
		t.Fatalf("limited: %v", got)
	}
	ctx = WithOnlyTools(context.Background(), []string{ToolDelegate})
	if got := toolNames(only(bot, OnlyTools(ctx))); !slices.Equal(got, []string{ToolDelegate, ToolTaskStop, ToolTaskTell}) {
		t.Fatalf("delegate: %v", got)
	}
}

// An answer has the tools of its own besides those it is limited to, also
// where the limit leaves it none of the agent's; without any it is the agent.
func TestOwnTools(t *testing.T) {
	quiet := func(context.Context, struct{}) (string, error) { return "", nil }
	bot := agent.New(promptLLM{}, "m", "", agent.NewTool("bash", "", nil, quiet), agent.NewTool("read", "", nil, quiet))
	if got := OwnTools(context.Background()); got != nil {
		t.Fatalf("tools of its own without any: %v", got)
	}
	if own(bot, OwnTools(context.Background())) != bot {
		t.Fatal("an answer without tools of its own is not the agent")
	}
	mine := []agent.Tool{agent.NewTool("ticket_close", "", nil, quiet)}
	ctx := WithOwnTools(context.Background(), mine)
	mine[0].Name = "changed" // what is handed over is kept as it was
	if got := toolNames(own(bot, OwnTools(ctx))); !slices.Equal(got, []string{"bash", "read", "ticket_close"}) {
		t.Fatalf("with a tool of its own: %v", got)
	}
	limited := WithOnlyTools(ctx, []string{"ticket_close"})
	if got := toolNames(own(only(bot, OnlyTools(limited)), OwnTools(limited))); !slices.Equal(got, []string{"ticket_close"}) {
		t.Fatalf("limited to its own: %v", got)
	}
}
