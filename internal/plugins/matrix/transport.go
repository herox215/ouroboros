package matrix

import (
	"context"
	"path/filepath"

	transport "ouroboros/internal/matrix"
)

func login(ctx context.Context, server, user, password string) (loginResult, error) {
	result, err := transport.Login(ctx, server, user, password)
	return loginResult{UserID: result.UserID, Token: result.Token, DeviceID: result.DeviceID}, err
}
func newTransport(_ context.Context, cfg configuration, s store) (connection, error) {
	client, err := transport.New(transport.Config{Homeserver: cfg.Homeserver, UserID: cfg.UserID, Token: cfg.Token, DeviceID: cfg.DeviceID, StateDir: filepath.Join(s.dir, "devices"), AllowedUsers: cfg.AllowedUsers})
	if err != nil {
		return nil, err
	}
	return &sdkConnection{client: client}, nil
}

type sdkConnection struct{ client *transport.Client }

func (c *sdkConnection) Run(ctx context.Context, receive func(context.Context, incoming) error) error {
	return c.client.Run(ctx, func(msgCtx context.Context, msg transport.Message) {
		// The SDK has already authenticated the sender and checked the full room
		// state. Host policy is checked once more at the session boundary.
		_ = receive(msgCtx, incoming{RoomID: msg.RoomID, Sender: msg.Sender, Text: msg.Text})
	})
}
func (c *sdkConnection) SendTo(ctx context.Context, room, peer, text string) error {
	return c.client.SendTo(ctx, room, peer, text)
}
func (c *sdkConnection) TypingTo(ctx context.Context, room, peer string, typing bool) error {
	return c.client.TypingTo(ctx, room, peer, typing)
}
func (c *sdkConnection) Close() error   { return c.client.Close() }
func (c *sdkConnection) Status() string { return c.client.Status() }
