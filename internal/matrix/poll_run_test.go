package matrix

import (
	"context"
	"fmt"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"ouroboros/plugin"
)

func TestRunPollContextAndSilentUpdates(t *testing.T) {
	for _, unstable := range []bool{false, true} {
		t.Run(fmt.Sprint(unstable), func(t *testing.T) {
			live := time.Now().Add(time.Hour).UnixMilli()
			prefix := "m.poll."
			if unstable {
				prefix = "org.matrix.msc3381.poll."
			}
			change := func(kind string, content map[string]any) func(*event.Event) {
				return func(e *event.Event) { e.Type = event.NewEventType(kind); e.Content = event.Content{Raw: content} }
			}
			start := change(prefix+"start", pollStartFixture(unstable))
			vote := pollUpdateFixture("response", "$vote", "@alice:test", "!dm:test", "$start", live+3, unstable, []string{"a"})
			end := pollUpdateFixture("end", "$end", "@alice:test", "!dm:test", "$start", live+5, unstable, nil)
			changes := map[id.EventID]func(*event.Event){
				"$start":     start,
				"$untrusted": func(e *event.Event) { start(e); e.Mautrix.TrustSource = nil },
				"$unsafe":    start,
				"$vote":      change(vote.Type.Type, vote.Content.Raw),
				"$end":       change(end.Type.Type, end.Content.Raw),
				"$location": func(e *event.Event) {
					e.Content = event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgLocation, GeoURI: "geo:1,2"}}
				},
			}
			crypt := &fakeCrypto{change: changes}
			useCrypto(t, crypt, nil)
			ids := []string{"$untrusted", "$before", "$start", "$vote", "$during", "$end", "$location", "$after"}
			events := make([]map[string]any, 0, len(ids))
			for i, v := range ids {
				events = append(events, encryptedEvent(v, "@alice:test", live+int64(i)))
			}
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{
				"!dm:test":     events,
				"!other:test":  {encryptedEvent("$other", "@alice:test", live)},
				"!unsafe:test": {encryptedEvent("$unsafe", "@alice:test", live)},
			}))
			h.room("!other:test", map[string]event.Membership{"@alice:test": event.MembershipJoin})
			h.room("!unsafe:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipJoin})
			got, _ := runUntilIdle(t, h, testClient(t, h.server.URL), func(_ context.Context, m Message) {
				if _, err := plugin.NewGatewayMetadata(m.Metadata); err != nil {
					t.Error(err)
				}
			})
			if len(got) != 5 {
				t.Fatalf("unexpected deliveries: %+v", got)
			}
			var direct []Message
			for _, m := range got {
				if m.RoomID == "!other:test" {
					if len(m.Metadata) != 0 {
						t.Fatal("cross-room context")
					}
					continue
				}
				direct = append(direct, m)
			}
			if len(direct) != 4 {
				t.Fatalf("direct callbacks: %+v", direct)
			}
			if len(direct[0].Metadata) != 0 {
				t.Fatal("untrusted poll accepted")
			}
			if direct[1].Text != "Poll: Lunch?\n1. Pizza\n2. Soup" {
				t.Fatalf("poll text: %q", direct[1].Text)
			}
			first := direct[1].Metadata["polls"].([]map[string]any)[0]
			during := direct[2].Metadata["polls"].([]map[string]any)[0]
			after := direct[3].Metadata["polls"].([]map[string]any)[0]
			if pollCount(first, 0) != 0 || pollCount(during, 0) != 1 || during["closed"] != false || after["closed"] != true {
				t.Fatalf("lifecycle: %v %v %v", first, during, after)
			}
			if direct[3].Metadata["location"].(map[string]any)["uri"] != "geo:1,2" {
				t.Fatal("lost location context")
			}
			if crypt.encrypted != 0 {
				t.Fatal("silent events sent replies")
			}
		})
	}
}

func TestPollIgnoresResponseBeforeStart(t *testing.T) {
	var tracker pollTracker
	tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, pollStartFixture(false)))
	tracker.update(pollUpdateFixture("response", "$v", "@a:x", "!r:x", "$p", 9, false, []string{"a"}))
	if pollCount(tracker.snapshot("!r:x", "@a:x")[0], 0) != 0 {
		t.Fatal("counted vote before poll start")
	}
}
