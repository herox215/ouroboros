package cli

import (
	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
)

// llm returns the model the node configured by cfg talks to. The plugin that
// provides it decides which API the agents use.
type llm func(cfg config.Config) agent.LLM

// openaiPlugin makes the agents talk to an OpenAI-compatible API. A plugin
// for another API provides an llm instead of this one.
func openaiPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "openai", Start: func(ctx *kernel.Context) error {
		kernel.Provide(ctx, llm(func(cfg config.Config) agent.LLM {
			return openai.New(cfg.BaseURL, cfg.APIKey)
		}))
		return nil
	}}
}

// assistantPlugin provides the blueprint of the agents and the assistant of
// the owner. The agents get every agent.Tool a plugin contributed, as far as
// the owner allows it in the configuration.
func assistantPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "assistant", Start: func(ctx *kernel.Context) error {
		var err error
		dir := use[home.Dir](ctx, &err)
		model := use[llm](ctx, &err)
		if err != nil {
			return err
		}
		builder := assistant.Builder{
			LLM:      model,
			Tools:    func() []agent.Tool { return kernel.All[agent.Tool](ctx) },
			Failures: dir.Failures(),
		}
		kernel.Provide(ctx, builder)
		kernel.Provide(ctx, assistant.New(dir.Config, builder))
		return nil
	}}
}
