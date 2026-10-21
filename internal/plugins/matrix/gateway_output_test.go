package matrix

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/guard"
	"ouroboros/plugin"
)

func TestGatewayOutputImageDestination(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	picture := []byte("\x89PNG\r\n\x1a\nfixture")
	if err := os.WriteFile(filepath.Join(root, "chart.png"), picture, 0600); err != nil {
		t.Fatal(err)
	}
	files := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	conn := &imageConnection{}
	c := &conversations{store: s, entries: map[string]*conversation{
		"a": {Room: "!a:test", Identity: plugin.GatewayIdentity{Subject: "@owner:test\n!a:test"}},
		"b": {Room: "!b:test", Identity: plugin.GatewayIdentity{Subject: "@other:test\n!b:test"}},
	}}
	running := &live{}
	running.set(guardedConnection{connection: conn, store: s, revision: "1"}, c)
	output := gatewayOutput(s, running, files)
	request := plugin.GatewayImageRequest{Path: "chart.png"}
	if _, err := output.SendImage(context.Background(), request); err == nil || !strings.Contains(err.Error(), "choose the room") {
		t.Fatalf("ambiguous destination: %v", err)
	}
	request.Destination = "!b:test"
	result, err := output.SendImage(context.Background(), request)
	if err != nil || !strings.Contains(result, `"sent":true`) {
		t.Fatalf("send: %s, %v", result, err)
	}
	if len(conn.rooms) != 1 || conn.rooms[0] != "!b:test" || conn.peers[0] != "@other:test" || conn.names[0] != "chart.png" || !bytes.Equal(conn.data[0], picture) {
		t.Fatalf("sent: %+v", conn)
	}
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	if _, err := output.SendImage(peer, request); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("peer image: %v", err)
	}
	if _, err := output.SetAvatar(peer, plugin.GatewayAvatarRequest{}); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("peer avatar: %v", err)
	}
	if _, err := output.SetAvatar(context.Background(), plugin.GatewayAvatarRequest{Path: "chart.png", Attachment: 1}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("avatar arguments: %v", err)
	}
	request.Path, request.Attachment = "", 2
	if _, err := output.SendImage(context.Background(), request); err == nil || !strings.Contains(err.Error(), "no such image attachment") {
		t.Fatalf("attachment: %v", err)
	}
	if len(conn.rooms) != 1 {
		t.Fatalf("unexpected sends: %d", len(conn.rooms))
	}
	if sendImage != plugin.GatewaySendImageTool || setAvatar != plugin.GatewaySetAvatarTool {
		t.Fatal("backend guard identities must match the visible gateway tools")
	}
}
