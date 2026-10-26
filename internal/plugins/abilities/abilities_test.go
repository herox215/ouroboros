package abilities

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

func TestAbilityContributionsDiscoveredAfterStartup(t *testing.T) {
	k := kernel.New()
	var pctx *plugin.Context
	err := k.Start(Plugin(), plugin.New("late-provider", func(ctx *plugin.Context, _ struct{}) error {
		pctx = ctx
		plugin.Add(ctx, plugin.Ability{Name: "fixture", Description: "late fixture", Instructions: "instructions", Tools: []plugin.Tool{
			plugin.NewTool("fixture_action", "harmless", nil, func(context.Context, struct{}) (string, error) { return "ok", nil }),
		}})
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	raw := plugin.All[plugin.Tool](pctx)
	if len(raw) != 2 {
		t.Fatalf("contributed %d tools", len(raw))
	}
	expanded, err := agent.ExpandAbilities(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded) != 3 || !strings.Contains(expanded[0].Description, "fixture: late fixture") {
		t.Fatalf("late catalog: %+v", expanded)
	}
	for _, tool := range raw {
		if !tool.OwnerOnly {
			t.Fatal("loader/inspector not owner only")
		}
		if _, err := tool.Run(t.Context(), []byte(`{"name":"fixture"}`)); err == nil {
			t.Fatal("out-of-conversation invocation succeeded")
		}
	}
	// Provider changes are visible to the next build, not cached at start.
	plugin.Add(pctx, plugin.Ability{Name: "second", Description: "new contribution"})
	expanded, err = agent.ExpandAbilities(raw)
	if err != nil || !strings.Contains(expanded[0].Description, "second: new contribution") {
		t.Fatalf("stale catalog: %v", err)
	}
}
