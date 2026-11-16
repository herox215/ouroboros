package assistant

import (
	"context"
	"slices"
	"strings"
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

	session, err := b.forOwner(cfg, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := b.forOwner(cfg, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(session); slices.Contains(names, "command") || !slices.Contains(names, "read") {
		t.Errorf("the sessions get %v", names)
	}
	if names := toolNames(thread); !slices.Contains(names, "command") || !slices.Contains(names, "read") {
		t.Errorf("the thread gets %v", names)
	}
	// In its answer the agent of the thread has its own tools and those the
	// owner allows it alone: the rest of the owner's tools it hands on.
	answering := inThread(WithThread(context.Background(), &fakeThread{}), thread, cfg)
	names := toolNames(answering)
	if !slices.Contains(names, "command") || !slices.Contains(names, ToolSessionMessage) || slices.Contains(names, "read") {
		t.Errorf("the thread answers with %v", names)
	}
	// The prompt tells of the thread's own tools only where there are some.
	if system, _ := answering.Prompt(); !strings.Contains(system, threadCommandsHint) {
		t.Errorf("the thread is not told of its own tools: %q", system)
	}
	quietAgent := agent.New(nil, "m", "prompt", everywhere)
	if system, _ := inThread(WithThread(context.Background(), &fakeThread{}), quietAgent, cfg).Prompt(); strings.Contains(system, threadCommandsHint) {
		t.Errorf("the thread is told of tools it has not: %q", system)
	}
	tools, err := b.ownerTools(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := threadHelper(b, cfg, forHelpers(tools))
	if err != nil {
		t.Fatal(err)
	}
	// A helper is not the thread: a thread-only tool is none of its tools.
	if names := toolNames(helper); slices.Contains(names, "command") {
		t.Errorf("a helper gets %v", names)
	}
}

// The thread is told of the board where it has its tools, and only there;
// they are no commands it runs itself, so they do not bring that hint.
func TestThreadIsToldOfTheBoard(t *testing.T) {
	cfg, _ := directNode(t)
	quiet := func(context.Context, struct{}) (string, error) { return "", nil }
	var tools []agent.Tool
	for _, name := range []string{ToolTicketCreate, ToolTicketComment, ToolBoardRead} {
		tool := agent.NewTool(name, "", nil, quiet)
		tool.ThreadOnly = true
		tools = append(tools, tool)
	}
	ctx := WithThread(context.Background(), &fakeThread{})
	system, _ := inThread(ctx, agent.New(nil, "m", "prompt", tools...), cfg).Prompt()
	if !strings.Contains(system, threadBoardHint) || strings.Contains(system, threadCommandsHint) {
		t.Errorf("the thread with the board: %q", system)
	}
	if names := toolNames(inThread(ctx, agent.New(nil, "m", "prompt", tools...), cfg)); !slices.Contains(names, ToolTicketCreate) || !slices.Contains(names, ToolSessionNew) {
		t.Errorf("the thread with the board answers with %v", names)
	}
	// Reading the board and commenting alone put nothing on it: no hint.
	system, _ = inThread(ctx, agent.New(nil, "m", "prompt", tools[1:]...), cfg).Prompt()
	if strings.Contains(system, threadBoardHint) || strings.Contains(system, threadCommandsHint) {
		t.Errorf("the thread without ticket_create: %q", system)
	}
	command := agent.NewTool("command", "", nil, quiet)
	command.ThreadOnly = true
	system, _ = inThread(ctx, agent.New(nil, "m", "prompt", append(tools, command)...), cfg).Prompt()
	if !strings.Contains(system, threadBoardHint) || !strings.Contains(system, threadCommandsHint) {
		t.Errorf("the thread with the board and a command: %q", system)
	}
	for _, name := range []string{ToolTicketCreate, ToolTicketComment, ToolBoardRead, ToolSessionNew} {
		if !strings.Contains(threadBoardHint, name) {
			t.Errorf("the hint does not name %s", name)
		}
	}
}
