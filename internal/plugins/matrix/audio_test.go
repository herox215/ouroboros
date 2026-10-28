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

type audioConnection struct {
	fakeConnection
	rooms, peers, names []string
	data                [][]byte
}

func (c *audioConnection) SendAudioTo(_ context.Context, room, peer, name string, data []byte) error {
	c.rooms, c.peers, c.names, c.data = append(c.rooms, room), append(c.peers, peer), append(c.names, name), append(c.data, data)
	return nil
}

func TestAudioToolSendsToTheRoomOfTheOwner(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	click := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00")
	if err := os.WriteFile(filepath.Join(root, "click.wav"), click, 0600); err != nil {
		t.Fatal(err)
	}
	g := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	l := &live{}
	tool := audioTool(l, g)
	if !tool.OwnerOnly {
		t.Fatal("tool must be owner-only")
	}
	args := json.RawMessage(`{"path":"click.wav"}`)
	run := func(ctx context.Context, args json.RawMessage, want string) {
		t.Helper()
		if _, err := tool.Run(ctx, args); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
	run(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), args, "owner-only")
	run(context.Background(), json.RawMessage(`{}`), "path of the audio file")
	run(context.Background(), args, "not connected")
	conn := &audioConnection{}
	c := &conversations{store: s, entries: map[string]*conversation{}}
	l.set(guardedConnection{connection: conn, store: s, revision: "1"}, c)
	run(context.Background(), args, "write to it from Matrix first")
	c.entries["a"] = &conversation{Room: "!a:test", Identity: plugin.GatewayIdentity{Subject: "@owner:test\n!a:test"}}
	if _, err := tool.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if len(conn.rooms) != 1 || conn.rooms[0] != "!a:test" || conn.peers[0] != "@owner:test" || conn.names[0] != "click.wav" || !bytes.Equal(conn.data[0], click) {
		t.Fatalf("sent %+v", conn)
	}
}
