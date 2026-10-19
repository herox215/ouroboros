package matrix

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// recordingConnection records what is sent over it as "room peer text". A
// send fails with the next error of fail, and sending runs sending, if set.
type recordingConnection struct {
	fakeConnection
	mu      sync.Mutex
	sent    []string
	fail    []error
	sending func()
}

func (r *recordingConnection) SendTo(_ context.Context, room, peer, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sending != nil {
		r.sending()
	}
	if len(r.fail) > 0 {
		err := r.fail[0]
		r.fail = r.fail[1:]
		if err != nil {
			return err
		}
	}
	r.sent = append(r.sent, room+" "+peer+" "+text)
	return nil
}

func (r *recordingConnection) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

func TestGuardChecksTheSettingsBeforeEveryPart(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	conn := &recordingConnection{}
	g := guardedConnection{connection: conn, store: s, revision: "1"}
	long := strings.Repeat("a", 12000) + "b"
	if err := g.SendTo(context.Background(), "!room:test", "@owner:test", long); err != nil {
		t.Fatal(err)
	}
	if want := []string{"!room:test @owner:test " + long[:12000], "!room:test @owner:test b"}; !slices.Equal(conn.messages(), want) {
		t.Fatalf("sent %d parts", len(conn.messages()))
	}
	// A part that cannot be sent ends the message.
	down := errors.New("homeserver down")
	conn.fail = []error{down}
	if err := g.SendTo(context.Background(), "!room:test", "@owner:test", long); !errors.Is(err, down) || len(conn.messages()) != 2 {
		t.Fatalf("after a failed part: %v, %d sent", err, len(conn.messages()))
	}
	// Settings that change while a long message is sent stop the rest.
	conn.sending = func() { s.save(configuration{Enabled: true, Revision: "2"}) }
	if err := g.SendTo(context.Background(), "!room:test", "@owner:test", long); err == nil || len(conn.messages()) != 3 {
		t.Fatalf("after the settings changed: %v, %d sent", err, len(conn.messages()))
	}
	conn.sending = nil
	typer := &typingConnection{}
	current := guardedConnection{connection: typer, store: s, revision: "2"}
	if err := current.TypingTo(context.Background(), "!room:test", "@owner:test", true); err != nil || !slices.Equal(typer.states, []bool{true}) {
		t.Fatalf("typing while current: %v %v", err, typer.states)
	}
	// Disabled or unreadable settings revoke access as a new revision does.
	for _, settings := range []string{`{"enabled": false, "revision": "2"}`, "{"} {
		if err := os.WriteFile(s.path("settings.json"), []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
		current.connection = conn
		if err := current.SendTo(context.Background(), "!room:test", "@owner:test", "must not leave"); err == nil {
			t.Fatalf("sent with settings %s", settings)
		}
		current.connection = typer
		if err := current.TypingTo(context.Background(), "!room:test", "@owner:test", true); err == nil {
			t.Fatalf("typing with settings %s", settings)
		}
	}
	if len(conn.messages()) != 3 || len(typer.states) != 1 {
		t.Fatal("revoked access went through")
	}
}
