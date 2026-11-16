package assistant

import (
	"context"
	"slices"

	"ouroboros/internal/agent"
)

// onlyKey is the key of the tools an answer is limited to, see WithOnlyTools.
type onlyKey struct{}

// WithOnlyTools limits an answer that runs with ctx to the tools of the
// owner's agent that names lists, like those a scheduled job names. Nil or
// empty leaves it all of them. A named tool the agent does not have is left
// out: the owner's lists may have changed since the names were saved.
func WithOnlyTools(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, onlyKey{}, slices.Clone(names))
}

// OnlyTools are the tools an answer that runs with ctx is limited to, see
// WithOnlyTools: nil for all of them.
func OnlyTools(ctx context.Context) []string {
	names, _ := ctx.Value(onlyKey{}).([]string)
	if len(names) == 0 {
		return nil
	}
	return names
}

// only returns bot with those of its tools that names lists, bot itself for
// none. With delegate come the tools that reach its tasks.
func only(bot *agent.Agent, names []string) *agent.Agent {
	if len(names) == 0 {
		return bot
	}
	keep := slices.Clone(names)
	if slices.Contains(keep, ToolDelegate) {
		keep = append(keep, ToolTaskStop, ToolTaskTell)
	}
	tools := slices.DeleteFunc(bot.Tools(), func(t agent.Tool) bool { return !slices.Contains(keep, t.Name) })
	return bot.WithTools(tools...)
}

// ownKey is the key of the tools an answer has for itself, see WithOwnTools.
type ownKey struct{}

// WithOwnTools gives an answer that runs with ctx tools of its own, besides
// those of the owner's agent: what only that answer is to do, like the tools
// of a ticket for the session that works on it. They are no tools of the
// agent: no other answer has them, and no helper.
func WithOwnTools(ctx context.Context, tools []agent.Tool) context.Context {
	return context.WithValue(ctx, ownKey{}, slices.Clone(tools))
}

// OwnTools are the tools an answer that runs with ctx has for itself, see
// WithOwnTools.
func OwnTools(ctx context.Context) []agent.Tool {
	tools, _ := ctx.Value(ownKey{}).([]agent.Tool)
	return tools
}

// own returns bot with tools besides its own, bot itself for none.
func own(bot *agent.Agent, tools []agent.Tool) *agent.Agent {
	if len(tools) == 0 {
		return bot
	}
	return bot.WithTools(append(bot.Tools(), tools...)...)
}
