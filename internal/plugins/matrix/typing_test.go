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
	for _, end := range []plugin.SessionKind{plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled, plugin.SessionRestart, plugin.SessionRestarted} {
		events := []plugin.SessionEvent{{Kind: plugin.SessionMessage}, {Kind: plugin.SessionStep}, {Kind: end}}
		if !answering("s1", events[:2]) || answering("s1", events) {
			t.Fatalf("wrong state for %v", end)
		}
	}
	events := []plugin.SessionEvent{{Kind: plugin.SessionMessage}, {Kind: plugin.SessionAsked}}
	if answering("s1", events) {
		t.Fatal("typing while waiting for owner")
	}
	events = append(events, plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true})
	if !answering("s1", events) {
		t.Fatal("did not resume after reply")
	}
	if answering("s1", nil) {
		t.Fatal("idle session is typing")
	}
	// The thread types while it answers what the owner said and has not
	// answered yet, also when it handed the message on, and not after its
	// answer: work that goes on then is background, and not typing.
	msg := plugin.SessionEvent{Kind: plugin.SessionMessage}
	work := plugin.SessionEvent{Kind: plugin.SessionWorking, N: 1}
	own := plugin.SessionEvent{Kind: plugin.SessionWorking, N: 1, Own: true}
	if !answering(plugin.ThreadID, []plugin.SessionEvent{msg, work}) || !answering(plugin.ThreadID, []plugin.SessionEvent{msg, own}) {
		t.Fatal("the thread does not type while it answers")
	}
	if answering(plugin.ThreadID, []plugin.SessionEvent{work}) || answering(plugin.ThreadID, []plugin.SessionEvent{msg, plugin.SessionEvent{Kind: plugin.SessionAnswer}, work}) {
		t.Fatal("the thread types while nothing works for it, or after its answer")
	}
	if !answering(plugin.ThreadID, []plugin.SessionEvent{msg, plugin.SessionEvent{Kind: plugin.SessionAnswer}, own}) {
		t.Fatal("the thread does not type while it answers the handed-on work")
	}
	asking := []plugin.SessionEvent{msg, plugin.SessionEvent{Kind: plugin.SessionAsked}, work}
	if answering(plugin.ThreadID, asking) || !answering(plugin.ThreadID, []plugin.SessionEvent{msg, plugin.SessionEvent{Kind: plugin.SessionAsked}, plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true}, work}) {
		t.Fatal("the thread types while it asks, or not once it was answered")
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

// A thread whose work has stopped after the message it was handed does not
// type: it owes no answer once work it handed on is done.
func TestThreadStopsTypingWhenItsWorkStops(t *testing.T) {
	msg := plugin.SessionEvent{Kind: plugin.SessionMessage}
	stopped := plugin.SessionEvent{Kind: plugin.SessionWorking, N: 0, Own: true}
	if answering(plugin.ThreadID, []plugin.SessionEvent{msg, stopped}) || answering(plugin.ThreadID, []plugin.SessionEvent{stopped}) {
		t.Fatal("the thread types while nothing works")
	}
}
