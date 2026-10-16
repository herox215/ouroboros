package matrix

import (
	"context"
	"errors"
)

type guardedConnection struct {
	connection
	store    store
	revision string
}

func (g guardedConnection) check() error {
	cfg, err := g.store.load()
	if err != nil || !cfg.Enabled || cfg.Revision != g.revision {
		return errors.New("gateway configuration changed")
	}
	return nil
}
func (g guardedConnection) TypingTo(ctx context.Context, room, peer string, typing bool) error {
	if err := g.check(); err != nil {
		return err
	}
	return g.connection.TypingTo(ctx, room, peer, typing)
}

func (g guardedConnection) SendTo(ctx context.Context, room, peer, text string) error {
	for _, part := range messageParts(text) {
		if err := g.check(); err != nil {
			return err
		}
		if err := g.connection.SendTo(ctx, room, peer, part); err != nil {
			return err
		}
	}
	return nil
}
