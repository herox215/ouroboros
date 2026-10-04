// Package discovery is the plugin that finds the nodes that are online.
package discovery

import (
	"time"

	"ouroboros/internal/discovery"
	"ouroboros/plugin"
)

// wait is how long the network is given to answer.
const wait = 1500 * time.Millisecond

// services are what the plugin provides.
type services struct {
	Scanner *discovery.Scanner
}

// Plugin provides the scanner that finds the nodes that answer on the UDP
// port. It asks every plugin.PeerSource a plugin contributed.
func Plugin(port int) plugin.Plugin {
	return plugin.NewService("discovery", func(ctx *plugin.Context, _ struct{}) (services, error) {
		return services{&discovery.Scanner{
			Port:    port,
			Wait:    wait,
			Sources: func() []discovery.PeerSource { return plugin.All[plugin.PeerSource](ctx) },
		}}, nil
	})
}
