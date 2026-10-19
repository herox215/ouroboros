// Package claudecode is the plugin that lets the agents answer within the
// Claude plan of the owner, through Claude Code: the provider "claude-code".
package claudecode

import (
	"ouroboros/internal/claudecode"
	"ouroboros/plugin"
)

// Plugin contributes the provider that answers through Claude Code, see
// package claudecode. It can be turned off like any plugin, as long as no
// access of the node signs in with it.
func Plugin(o claudecode.Options) plugin.Plugin {
	return plugin.New(claudecode.Name, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, claudecode.Provider(o))
		return nil
	})
}
