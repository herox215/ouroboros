package tui

import (
	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
	"ouroboros/internal/tui"
)

// Backend is what the terminal UI of the node in dir controls, with exe for
// the binary of the node.
func Backend(dir home.Dir, network *peers.Network, bot *assistant.Assistant, exe func() (string, error)) tui.Backend {
	return &ui{home: dir, local: node.Local{Home: dir, Exe: exe}, assistant: bot, network: network}
}
