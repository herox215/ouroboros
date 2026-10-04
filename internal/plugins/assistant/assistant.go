// Package assistant is the plugin that builds the agents of the node.
package assistant

import (
	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	LLM plugin.LLM
}

// services are what the plugin provides.
type services struct {
	Builder   assistant.Builder
	Assistant *assistant.Assistant
}

// Plugin provides the blueprint of the agents of the node in dir and the
// assistant of its owner. The agents get every plugin.Tool a plugin
// contributed, as far as the owner allows it in the configuration.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("assistant", func(ctx *plugin.Context, n needs) (services, error) {
		builder := assistant.Builder{
			LLM:      n.LLM,
			Tools:    func() []agent.Tool { return plugin.All[plugin.Tool](ctx) },
			Failures: dir.Failures(),
		}
		return services{builder, assistant.New(dir.Config, builder)}, nil
	})
}
