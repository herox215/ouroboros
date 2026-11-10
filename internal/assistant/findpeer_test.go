package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// find_peer needs a topic, or a peer and a message; with neither it refuses
// before it reaches for the network.
func TestFindPeerNeedsArguments(t *testing.T) {
	tool := findPeer(nil, func() string { return "" })
	_, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "needs") {
		t.Fatalf("err %v", err)
	}
}
