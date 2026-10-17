package matrix

import (
	"context"
	"path/filepath"

	"ouroboros/internal/home"
	"ouroboros/plugin"
)

type needs struct{ Sessions plugin.Sessions }

// Plugin adds an external messaging gateway, not another default FrontEnd.
// Only the serving node owns the sync loop; settings in local frontends write
// the profile-local store, which the node reloads without a process restart.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.New("matrix", func(ctx *plugin.Context, n needs) error {
		s := store{filepath.Join(dir.Gateways(), "matrix")}
		c := &controller{store: s, receive: receive}
		c.connect = func(runCtx context.Context, cfg configuration) (connection, error) {
			conversations, err := openConversations(s, n.Sessions)
			if err != nil {
				return nil, err
			}
			transport, err := newTransport(runCtx, cfg, s)
			if err != nil {
				return nil, err
			}
			return &sessionConnection{connection: transport, conversations: conversations, store: s, revision: cfg.Revision}, nil
		}
		plugin.Add(ctx, plugin.Gateway{Name: "matrix", Title: "Matrix",
			Settings: func(context.Context) (plugin.GatewaySettings, error) { return s.snapshot() },
			Save: func(saveCtx context.Context, change plugin.GatewayChange) error {
				return s.change(saveCtx, change, login)
			},
		})
		plugin.Add(ctx, plugin.Serving{Start: func() {
			if err := s.create(); err != nil {
				dir.Log("matrix gateway state cannot be opened: %v", err)
				return
			}
			c.start()
		}, Stop: c.stop})
		ctx.OnStop(c.stop)
		return nil
	})
}

// receive hands a message to the conversations of the connection it came
// over. The controller passes a revocation guard around the active connection:
// a message that came any other way is refused.
func receive(ctx context.Context, conn connection, msg incoming) error {
	wrapped, ok := conn.(guardedConnection)
	if !ok {
		return plugin.ErrGatewayDenied
	}
	active, ok := wrapped.connection.(*sessionConnection)
	if !ok {
		return plugin.ErrGatewayDenied
	}
	return active.conversations.receive(ctx, conn, msg)
}

func (s *sessionConnection) Status() string {
	if state, ok := s.connection.(interface{ Status() string }); ok {
		return state.Status()
	}
	return "Synchronizing"
}

type sessionConnection struct {
	connection
	conversations *conversations
	store         store
	revision      string
}

func (s *sessionConnection) Run(ctx context.Context, receive func(context.Context, incoming) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	guarded := guardedConnection{connection: s.connection, store: s.store, revision: s.revision}
	go func() { defer close(done); s.conversations.poll(runCtx, guarded) }()
	err := s.connection.Run(runCtx, receive)
	cancel()
	<-done
	return err
}
