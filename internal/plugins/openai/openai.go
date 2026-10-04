// Package openai is the plugin that makes the agents talk to an
// OpenAI-compatible API.
package openai

import (
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// services are what the plugin provides.
type services struct {
	LLM plugin.LLM
}

// Plugin makes the agents talk to an OpenAI-compatible API. A plugin for
// another API provides a plugin.LLM instead of this one.
func Plugin() plugin.Plugin {
	return plugin.NewService("openai", func(*plugin.Context, struct{}) (services, error) {
		return services{func(at plugin.Endpoint) plugin.Model {
			return openai.New(at.BaseURL, at.APIKey)
		}}, nil
	})
}
