package cli

import (
	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
)

// peersPlugin provides the network of other nodes and contributes the tools
// with which an agent reaches it: list_peers and ask_peer.
func peersPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "peers", Start: func(ctx *kernel.Context) error {
		var err error
		dir := use[home.Dir](ctx, &err)
		local := use[node.Local](ctx, &err)
		scanner := use[*discovery.Scanner](ctx, &err)
		if err != nil {
			return err
		}
		network := &peers.Network{Finder: scanner, Contacts: dir.Contacts(), Client: local.Client}
		kernel.Provide(ctx, network)

		selfID := func() string {
			cfg, _ := dir.Config() // without a configuration no node is this one
			return cfg.ID
		}
		for _, tool := range assistant.PeerTools(network, selfID) {
			kernel.Add[agent.Tool](ctx, tool)
		}
		return nil
	}}
}
