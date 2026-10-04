package plugin_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
	"ouroboros/internal/peers"
	"ouroboros/plugin"
)

// A plugin opens channels through the network of the node, which opens them
// to connected peers and to nobody else. How a channel behaves between two
// nodes is tested in package a2a and, with whole nodes, in internal/cli.
func TestOpenIsForConnectedPeers(t *testing.T) {
	alpha := discovery.Node{ID: "abc123", Name: "alpha", PublicKey: "pk-alpha", Host: "127.0.0.1", Port: 1}
	n := plugin.NetworkOf(&peers.Network{
		Finder:   finder{alpha},
		Contacts: contacts.Open(filepath.Join(t.TempDir(), "contacts.json")),
		Client:   func() (*a2a.Client, error) { return nil, errors.New("no client") },
	})
	if ch, err := n.Open(context.Background(), alpha, "example/checksum"); !errors.Is(err, plugin.ErrNotConnected) || ch != nil {
		t.Fatalf("a channel to a node that is not connected: %v, %v", ch, err)
	}
}

// What ouro closes a channel with is told apart with errors.Is, on the end
// that was closed and on the other, where the reason arrived as text.
func TestChannelErrors(t *testing.T) {
	for _, reason := range []error{plugin.ErrIdle, plugin.ErrDisconnected, plugin.ErrStopped} {
		var closed *plugin.ChannelClosed
		if !errors.As(reason, &closed) || closed.Reason == "" {
			t.Fatalf("%v gives no reason", reason)
		}
		arrived := &plugin.ChannelClosed{Reason: closed.Reason}
		if !errors.Is(arrived, reason) || errors.Is(arrived, plugin.ErrChannelClosed) || errors.Is(arrived, &plugin.ChannelClosed{Reason: "other"}) {
			t.Errorf("%v is not told apart", reason)
		}
	}
	if plugin.MaxMessage != 1<<20 || plugin.MaxChannelsPerPeer != 8 || plugin.ChannelIdleTimeout != 5*time.Minute {
		t.Errorf("limits: %d bytes, %d channels, idle after %v", plugin.MaxMessage, plugin.MaxChannelsPerPeer, plugin.ChannelIdleTimeout)
	}
	if plugin.ErrTooLarge == nil || plugin.CodeTooManyChannels == plugin.CodeUnauthorized {
		t.Error("errors of a channel are missing")
	}
}
