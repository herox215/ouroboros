package node

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
)

// The node opens the channels that were added for the peers its owner trusts,
// and when it stops it closes them: a channel is no request that ends by
// itself.
func TestRunClosesChannelsWhenStopped(t *testing.T) {
	dir := configured(t)
	peer := identity.Generate()
	if err := dir.Contacts().AddTrusted(peer.Public(), "beta", ""); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	stopping := make(chan struct{})
	s := testServer(dir)
	s.Channels = func() []a2a.ChannelHandler {
		return []a2a.ChannelHandler{{Name: "test/echo", Handle: func(_ context.Context, ch *a2a.Channel) {
			for {
				msg, err := ch.Receive()
				if err != nil {
					ended <- err
					return
				}
				ch.Send(msg)
			}
		}}}
	}
	s.OnStop = func() { close(stopping) }
	p, stop := run(t, s)

	id, _ := dir.Identity()
	client := &a2a.Client{Identity: peer}
	ch, err := client.Open(context.Background(), fmt.Sprintf("https://127.0.0.1:%d/", p.Port), id.Public(), "test/echo")
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if err := ch.Send([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if msg, err := ch.Receive(); err != nil || string(msg) != "hello" {
		t.Fatalf("got %q, %v", msg, err)
	}
	select {
	case <-stopping:
		t.Fatal("the node stops before it was told to")
	default:
	}

	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Receive(); !errors.Is(err, a2a.ErrStopped) {
		t.Fatalf("the channel of a node that stopped: %v", err)
	}
	if err := <-ended; err != a2a.ErrStopped {
		t.Fatalf("the handler ended with %v", err)
	}
	<-stopping
}

// The node has no message/stream, which is what A2A calls streaming, and
// says so. Its channels are something of its own.
func TestCardAnnouncesChannelsNotStreaming(t *testing.T) {
	capabilities := card(config.Config{Name: "alpha", Model: "gpt"}, "1.2.3").Capabilities
	extensions := capabilities.Extensions
	if capabilities.Streaming || len(extensions) != 1 || extensions[0].URI != a2a.ChannelExtensionURI || extensions[0].Required {
		t.Errorf("capabilities %+v", capabilities)
	}
}
