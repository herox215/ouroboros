package assistant

import (
	"context"
	"slices"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

// toolNames are the tools the agent sends with each call.
func toolNames(bot *agent.Agent) []string {
	_, tools := bot.Prompt()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Function.Name)
	}
	return names
}

// A tool that is for the thread alone is in the thread's agent and not in the
// sessions', though the owner allows it for both, and a helper gets neither.
func TestThreadOnlyTool(t *testing.T) {
	cfg, b := directNode(t)
	b.LLM = func(string) (agent.LLM, error) { return promptLLM{}, nil }
	cfg.Tools = &config.Tools{H2A: config.Named(config.AllTools)}
	quiet := func(context.Context, struct{}) (string, error) { return "", nil }
	threadOnly := agent.NewTool("command", "", nil, quiet)
	threadOnly.ThreadOnly = true
	everywhere := agent.NewTool("read", "", nil, quiet)
	b.Tools = func() []agent.Tool { return []agent.Tool{threadOnly, everywhere} }

	session, err := b.forOwner(cfg, false, false)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := b.forOwner(cfg, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(session); slices.Contains(names, "command") || !slices.Contains(names, "read") {
		t.Errorf("the sessions get %v", names)
	}
	if names := toolNames(thread); !slices.Contains(names, "command") || !slices.Contains(names, "read") {
		t.Errorf("the thread gets %v", names)
	}
	tools, err := b.ownerTools(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := b.helper(cfg, forHelpers(tools))
	if err != nil {
		t.Fatal(err)
	}
	// A helper is not the thread: a thread-only tool is none of its tools.
	if names := toolNames(helper); slices.Contains(names, "command") {
		t.Errorf("a helper gets %v", names)
	}
}
