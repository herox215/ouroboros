// Package session is the plugin that holds the conversations of the owner
// with the agent of the node: the sessions. The node runs their answers
// (session.Service) and keeps them in its archive, so that they outlive it;
// the front ends reach them over the socket of the node (session.Client).
package session

import (
	"strings"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
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
// assistant: those in the archive of the node too, and the conversations of
// peers in it to read. A peer is named as the contacts in dir name it.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("sessions", func(ctx *plugin.Context, n needs) (services, error) {
		s := In(dir, n.Assistant)
		ctx.OnStop(s.Close)
		s.ServicesFrom(func() []session.Services { return plugin.All[plugin.Services](ctx) })
		return services{s, session.NewClient(dir)}, nil
	})
}

// In returns the Service of the sessions of the node in dir, with a.
func In(dir home.Dir, a *assistant.Assistant) *session.Service {
	s := session.New(a)
	s.Workspace = dir.WorkingDir()
	s.Archive(history.Open(dir.History()), dir.Log, func(party string) string {
		data, _ := dir.Contacts().Snapshot() // a peer that is not known is named by its party
		for _, c := range data.Trusted {
			if history.Party(c.PublicKey) == party {
				return c.Name
			}
		}
		return "unknown peer " + strings.TrimPrefix(party, "peer-")[:8]
	})
	return s
}
