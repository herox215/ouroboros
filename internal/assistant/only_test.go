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
