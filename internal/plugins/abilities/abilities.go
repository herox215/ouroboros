// Package abilities contributes discovery and conversation-local activation of
// plugin.Ability values. It must be installed alongside ability providers.
package abilities

import (
	"context"
	"ouroboros/internal/agent"
	"ouroboros/plugin"
)

type args struct {
	Name string `json:"name"`
}

// Plugin contributes owner-only ability_load and ability_inspect tools. The
// catalog is discovered when the assistant selects tools, not during startup.
func Plugin() plugin.Plugin {
	return plugin.New("abilities", func(ctx *plugin.Context, _ struct{}) error {
		params := []plugin.Param{{Name: "name", Description: "The ability name from the catalog"}}
		load := plugin.NewTool("ability_load", "Load an ability's configured tools and instructions for this conversation only, starting on the next model call. Loading never overrides tool settings, permission checks or the allowlist. Dependencies on ordinary tools require separate permission. After restoring saved history, load again before using ability tools; old calls are not grants.", params,
			func(ctx context.Context, a args) (string, error) { return agent.LoadAbility(ctx, a.Name) })
		load.OwnerOnly = true
		load.AbilityCatalog = func() []plugin.Ability { return plugin.All[plugin.Ability](ctx) }
		inspect := plugin.NewTool("ability_inspect", "Inspect an ability's instructions and allowed/withheld tool names without loading it. Use a name from ability_load's catalog.", params,
			func(ctx context.Context, a args) (string, error) { return agent.InspectAbility(ctx, a.Name) })
		inspect.OwnerOnly = true
		plugin.Add(ctx, load)
		plugin.Add(ctx, inspect)
		return nil
	})
}
