// Package tailscale is the plugin that finds the nodes on the tailnet of this
// host.
package tailscale

import (
	"ouroboros/internal/discovery"
	"ouroboros/plugin"
)

// Plugin makes the nodes on the tailnet of this host findable, which
// broadcast does not reach. Another way to find nodes is a plugin like this.
func Plugin() plugin.Plugin {
	return plugin.New("tailscale", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.PeerSource(discovery.TailscalePeers))
		return nil
	})
}
