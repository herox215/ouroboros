// Package peers is the plugin that connects the node to the others: it
// provides the network and the tools with which an agent reaches it.
package peers

import (
	"context"
	"sync"

	"ouroboros/internal/assistant"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/peers"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	Scanner  *discovery.Scanner
	Sessions *session.Service
}

// services are what the plugin provides.
type services struct {
	Network *plugin.Network
}

// Plugin provides network, the other nodes as the node in dir reaches them,
// and contributes the tools list_peers and ask_peer, and what a front end
// shows of a call of ask_peer: which peer the agent is waiting for.
//
// network is part of the frame of ouro: it holds the contacts and signs as
// the node. The plugin tells it how nodes are found and hands it on as a
// plugin.Network, through which neither can be had.
func Plugin(dir home.Dir, network *peers.Network) plugin.Plugin {
	return plugin.NewService("peers", func(ctx *plugin.Context, n needs) (services, error) {
		network.Finder = n.Scanner
		approvalCtx, cancel := context.WithCancel(context.Background())
		var start sync.Once
		var running sync.WaitGroup
		stop := func() { cancel(); running.Wait() }
		ctx.OnStop(stop)
		plugin.Add(ctx, plugin.Serving{Start: func() {
			start.Do(func() {
				running.Add(1)
				go func() { defer running.Done(); network.Approvals(approvalCtx, n.Sessions.OwnerQuestion, dir.Log) }()
			})
		}, Stop: stop})

		selfID := func() string {
			cfg, _ := dir.Config() // without a configuration no node is this one
			return cfg.ID()
		}
		for _, tool := range assistant.PeerTools(network, selfID) {
			plugin.Add(ctx, tool)
		}
		// The agent may name the peer by its ID: the owner is shown its name.
		plugin.Add(ctx, plugin.ToolView{Tool: assistant.ToolAskPeer, Show: assistant.AskingPeerAs(network.NameOf),
			Detail: assistant.HowItEnded})
		return services{plugin.NetworkOf(network)}, nil
	})
}
