package matrix

import (
	"context"
	"path/filepath"

	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
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
		_ = receive(msgCtx, incoming{RoomID: msg.RoomID, Sender: msg.Sender, Text: msg.Text, Images: imagesOf(msg), Attachments: attachmentsOf(msg), Metadata: msg.Metadata})
	})
}

// imagesOf hands the images of a message on with their type and data.
func imagesOf(msg transport.Message) []plugin.Image {
	images := make([]plugin.Image, len(msg.Images))
	for i, img := range msg.Images {
		images[i] = plugin.Image{Type: img.MIMEType, Data: img.Data}
	}
	return images
}

func attachmentsOf(msg transport.Message) []plugin.Attachment {
	files := make([]plugin.Attachment, len(msg.Attachments))
	for i, file := range msg.Attachments {
		files[i] = plugin.Attachment{Name: file.Name, Type: file.MIMEType, Data: file.Data}
	}
	return files
}

func (c *sdkConnection) SendTo(ctx context.Context, room, peer, text string) error {
	return c.client.SendTo(ctx, room, peer, text)
}
func (c *sdkConnection) SendImageTo(ctx context.Context, room, peer, name string, data []byte) error {
	return c.client.SendImageTo(ctx, room, peer, name, data)
}
func (c *sdkConnection) SendFileTo(ctx context.Context, room, peer, name string, data []byte) error {
	return c.client.SendFileTo(ctx, room, peer, name, data)
}
func (c *sdkConnection) VotePollTo(ctx context.Context, room, peer, pollID string, optionIDs []string) error {
	return c.client.VotePollTo(ctx, room, peer, pollID, optionIDs)
}
func (c *sdkConnection) TypingTo(ctx context.Context, room, peer string, typing bool) error {
	return c.client.TypingTo(ctx, room, peer, typing)
}
func (c *sdkConnection) Close() error   { return c.client.Close() }
func (c *sdkConnection) Status() string { return c.client.Status() }
