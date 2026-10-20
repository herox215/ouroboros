package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/guard"
	"ouroboros/plugin"
)

type imageConnection struct {
	fakeConnection
	rooms, peers, names []string
	data                [][]byte
}

func (c *imageConnection) SendImageTo(_ context.Context, room, peer, name string, data []byte) error {
	c.rooms, c.peers, c.names, c.data = append(c.rooms, room), append(c.peers, peer), append(c.names, name), append(c.data, data)
	return nil
}

func TestImageToolSendsToTheRoomOfTheOwner(t *testing.T) {
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
	g := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	l := &live{}
	tool := imageTool(l, g)
	if !tool.OwnerOnly {
		t.Fatal("tool must be owner-only")
	}
	args := json.RawMessage(`{"path":"chart.png"}`)
	run := func(ctx context.Context, args json.RawMessage, want string) {
		t.Helper()
		if _, err := tool.Run(ctx, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	run(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), args, "owner-only")
	run(context.Background(), json.RawMessage(`{}`), "either a path or an attachment")
	run(context.Background(), args, "not connected")
	conn := &imageConnection{}
	c := &conversations{store: s, entries: map[string]*conversation{}}
	l.set(guardedConnection{connection: conn, store: s, revision: "1"}, c)
	run(context.Background(), args, "write to it from Matrix first")
	c.entries["a"] = &conversation{Room: "!a:test", Identity: plugin.GatewayIdentity{Subject: "@owner:test\n!a:test"}}
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if len(conn.rooms) != 1 || conn.rooms[0] != "!a:test" || conn.peers[0] != "@owner:test" || conn.names[0] != "chart.png" || !bytes.Equal(conn.data[0], picture) {
		t.Fatalf("sent %+v", conn)
	}
	// With two rooms it does not guess.
	c.entries["b"] = &conversation{Room: "!b:test", Identity: plugin.GatewayIdentity{Subject: "@other:test\n!b:test"}}
	run(context.Background(), args, "choose the room: one of !a:test, !b:test")
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"path":"chart.png","room":"!b:test"}`)); err != nil || conn.peers[1] != "@other:test" {
		t.Fatalf("room b: %v", err)
	}
	run(context.Background(), json.RawMessage(`{"attachment":1,"room":"!b:test"}`), "no such image attachment")
	// A settings change revokes the connection at once.
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "2"}); err != nil {
		t.Fatal(err)
	}
	run(context.Background(), json.RawMessage(`{"path":"chart.png","room":"!b:test"}`), "configuration changed")
	other := &conversations{}
	l.clear(other)
	l.clear(c)
	run(context.Background(), args, "not connected")
	if len(conn.rooms) != 2 {
		t.Fatalf("sent %d images", len(conn.rooms))
	}
}
