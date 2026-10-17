package matrix

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ouroboros/plugin"
)

type typingConnection struct {
	fakeConnection
	states []bool
	fail   bool
}

func (c *typingConnection) TypingTo(ctx context.Context, room, peer string, active bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if room != "!room:test" || peer != "@owner:test" {
		return errors.New("wrong room or peer")
	}
	c.states = append(c.states, active)
	if c.fail {
		return errors.New("unavailable")
	}
	return nil
}

func TestAnswering(t *testing.T) {
	for _, end := range []plugin.SessionKind{plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled, plugin.SessionRestart} {
		events := []plugin.SessionEvent{{Kind: plugin.SessionMessage}, {Kind: plugin.SessionStep}, {Kind: end}}
		if !answering(events[:2]) || answering(events) {
			t.Fatalf("wrong state for %v", end)
		}
	}
	events := []plugin.SessionEvent{{Kind: plugin.SessionMessage}, {Kind: plugin.SessionAsked}}
	if answering(events) {
		t.Fatal("typing while waiting for owner")
	}
	events = append(events, plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true})
	if !answering(events) {
		t.Fatal("did not resume after reply")
	}
	if answering(nil) {
		t.Fatal("idle session is typing")
	}
}

func TestTypingRenewStopAndRetry(t *testing.T) {
	conn := &typingConnection{}
	entry := &conversation{Room: "!room:test", Identity: plugin.GatewayIdentity{Subject: "@owner:test\n!room:test"}}
	states := typing{}
	now := time.Now()
	ctx := context.Background()
	states.update(ctx, conn, entry, false, now)
	states.update(ctx, conn, entry, true, now)
	states.update(ctx, conn, entry, true, now.Add(time.Second))
	states.update(ctx, conn, entry, true, now.Add(15*time.Second))
	states.update(ctx, conn, entry, false, now.Add(16*time.Second))
	states.update(ctx, conn, entry, false, now.Add(17*time.Second))
	if !reflect.DeepEqual(conn.states, []bool{true, true, false}) {
		t.Fatalf("notifications: %v", conn.states)
	}
	conn.fail = true
	states.update(ctx, conn, entry, true, now)
	if len(states) != 0 {
		t.Fatal("failed notification remembered as sent")
	}
	conn.fail = false
	states.update(ctx, conn, entry, true, now)
	states.stop(conn)
	if len(states) != 0 || conn.states[len(conn.states)-1] {
		t.Fatal("disconnect did not clear typing")
	}
}

func TestTypingGuardRevocation(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Revision: "new"}); err != nil {
		t.Fatal(err)
	}
	conn := &typingConnection{}
	guarded := guardedConnection{connection: conn, store: s, revision: "old"}
	if err := guarded.TypingTo(context.Background(), "!room:test", "@owner:test", true); err == nil {
		t.Fatal("revoked connection sent typing")
	}
	if len(conn.states) != 0 {
		t.Fatal("typing escaped guard")
	}
}

func TestTypingNeedsThePeerOfTheConversation(t *testing.T) {
	conn := &typingConnection{}
	entry := &conversation{Room: "!room:test", Identity: plugin.GatewayIdentity{Subject: "@owner:test"}}
	states := typing{}
	states.update(context.Background(), conn, entry, true, time.Now())
	if len(conn.states) != 0 || len(states) != 0 {
		t.Fatalf("typing without a peer: %v", conn.states)
	}
}
