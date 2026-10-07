// Package session is the plugin that holds the conversations of the owner
// with the agent of the node: the sessions. The node runs their answers
// (session.Service); the front ends reach them over the socket of the node
// (session.Client).
package session

import (
	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	Assistant *assistant.Assistant
}

// services are what the plugin provides.
type services struct {
	// Service holds the sessions in the process of the node, which serves
	// it on its socket.
	Service *session.Service
	// Sessions are the sessions for the front ends: the client of the
	// socket of the node in dir.
	Sessions plugin.Sessions
}

// Plugin provides the sessions of the owner of the node in dir, with its
// assistant.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("sessions", func(_ *plugin.Context, n needs) (services, error) {
		return services{session.New(n.Assistant), session.NewClient(dir)}, nil
	})
}
