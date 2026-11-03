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
