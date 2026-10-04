package cli

import (
	"time"

	"ouroboros/internal/discovery"
	"ouroboros/internal/kernel"
)

// discoverWait is how long the network is given to answer.
const discoverWait = 1500 * time.Millisecond

// discoveryPlugin provides the scanner that finds the nodes that are online.
// It asks every discovery.PeerSource a plugin contributed.
func discoveryPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "discovery", Start: func(ctx *kernel.Context) error {
		s, err := kernel.Use[Settings](ctx)
		if err != nil {
			return err
		}
		kernel.Provide(ctx, &discovery.Scanner{
			Port:    s.DiscoPort,
			Wait:    discoverWait,
			Sources: func() []discovery.PeerSource { return kernel.All[discovery.PeerSource](ctx) },
		})
		return nil
	}}
}

// tailscalePlugin makes the nodes on the tailnet of this host findable, which
// broadcast does not reach. Another way to find nodes is a plugin like this.
func tailscalePlugin() kernel.Plugin {
	return kernel.Plugin{Name: "tailscale", Start: func(ctx *kernel.Context) error {
		kernel.Add(ctx, discovery.PeerSource(discovery.TailscalePeers))
		return nil
	}}
}
