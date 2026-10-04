package tailscale_test

import (
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/discovery"
	discoveryplugin "ouroboros/internal/plugins/discovery"
	"ouroboros/internal/plugins/tailscale"
)

// The plugin adds the tailnet to where the scanner looks for nodes.
func TestTailnetIsASource(t *testing.T) {
	app, err := cli.NewWith(discoveryplugin.Plugin(4242))
	if err != nil {
		t.Fatal(err)
	}
	if scanner, _ := cli.Service[*discovery.Scanner](app); len(scanner.Sources()) != 0 {
		t.Fatal("without the plugin there is no source")
	}
	app, err = cli.NewWith(discoveryplugin.Plugin(4242), tailscale.Plugin())
	if err != nil {
		t.Fatal(err)
	}
	if scanner, _ := cli.Service[*discovery.Scanner](app); len(scanner.Sources()) != 1 {
		t.Fatal("the tailnet should be a source")
	}
}
