package chatgpt_test

import (
	"testing"

	"ouroboros/internal/chatgpt"
	"ouroboros/internal/cli"
	chatgptplugin "ouroboros/internal/plugins/chatgpt"
	"ouroboros/plugin"
)

// The plugin contributes the provider that signs in with ChatGPT, under its
// own name: an access names its provider, and the plugin of that name is
// what must not be turned off while the access is there.
func TestContributesTheProvider(t *testing.T) {
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	p := chatgptplugin.Plugin(chatgpt.Options{})
	if _, err := cli.NewWith(p, spy); err != nil {
		t.Fatal(err)
	}
	providers := plugin.All[plugin.Provider](ctx)
	if len(providers) != 1 || providers[0].Name != "chatgpt" || providers[0].Name != p.Name ||
		providers[0].Login == nil || providers[0].Models == nil || providers[0].Model == nil || providers[0].Logout == nil {
		t.Fatalf("providers %+v", providers)
	}
}
