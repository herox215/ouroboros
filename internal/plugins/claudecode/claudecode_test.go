package claudecode_test

import (
	"testing"

	"ouroboros/internal/claudecode"
	"ouroboros/internal/cli"
	claudecodeplugin "ouroboros/internal/plugins/claudecode"
	"ouroboros/plugin"
)

// The plugin contributes the provider that answers through Claude Code,
// under its own name: an access names its provider, and the plugin of that
// name is what must not be turned off while the access is there.
func TestContributesTheProvider(t *testing.T) {
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	p := claudecodeplugin.Plugin(claudecode.Options{})
	if _, err := cli.NewWith(p, spy); err != nil {
		t.Fatal(err)
	}
	providers := plugin.All[plugin.Provider](ctx)
	if len(providers) != 1 || providers[0].Name != "claude-code" || providers[0].Name != p.Name ||
		providers[0].Login == nil || providers[0].Models == nil || providers[0].Usage == nil || providers[0].Model == nil {
		t.Fatalf("providers %+v", providers)
	}
}
