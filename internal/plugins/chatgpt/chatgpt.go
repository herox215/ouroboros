// Package chatgpt is the plugin that lets the owner sign in with ChatGPT:
// the provider "chatgpt", with which the agents answer within the ChatGPT
// plan of the owner instead of with an API key.
package chatgpt

import (
	"ouroboros/internal/chatgpt"
	"ouroboros/plugin"
)

// Plugin contributes the provider that signs in with ChatGPT, see package
// chatgpt. It can be turned off like any plugin, as long as no access of the
// node signs in with it.
func Plugin(o chatgpt.Options) plugin.Plugin {
	return plugin.New(chatgpt.Name, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, chatgpt.Provider(o))
		return nil
	})
}
