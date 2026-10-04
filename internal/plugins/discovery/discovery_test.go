package discovery_test

import (
	"context"
	"slices"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/discovery"
	discoveryplugin "ouroboros/internal/plugins/discovery"
	"ouroboros/plugin"
)

// The scanner looks on the port it is given and asks every source a plugin
// contributed, whenever it is used.
func TestScannerAsksTheSourcesOfPlugins(t *testing.T) {
	vpn := plugin.New("vpn", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.PeerSource(func(context.Context) []string { return []string{"10.0.0.7"} }))
		return nil
	})
	app, err := cli.NewWith(vpn, discoveryplugin.Plugin(4242))
	if err != nil {
		t.Fatal(err)
	}
	scanner, ok := cli.Service[*discovery.Scanner](app)
	if !ok || scanner.Port != 4242 || scanner.Wait <= 0 {
		t.Fatalf("scanner %+v, %v", scanner, ok)
	}
	sources := scanner.Sources()
	if len(sources) != 1 || !slices.Equal(sources[0](context.Background()), []string{"10.0.0.7"}) {
		t.Fatalf("%d sources", len(sources))
	}
}
