package matrix

import (
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPlaintextBeaconAnnouncementsWithEncryptedCoordinates(t *testing.T) {
	for _, initialState := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeline", true: "initial state"}[initialState], func(t *testing.T) {
			now := time.Now().UnixMilli()
			live := now + int64(time.Hour/time.Millisecond)
			info := func(id string, active bool, at int64) map[string]any {
				return map[string]any{"type": "org.matrix.msc3672.beacon_info", "state_key": "@alice:test", "event_id": id, "sender": "@alice:test", "origin_server_ts": at, "content": map[string]any{"live": active, "timeout": 60000, "org.matrix.msc3488.ts": now}}
			}
			point := func(e *event.Event) {
				e.Type = event.NewEventType("org.matrix.msc3672.beacon")
				e.Content = event.Content{Raw: map[string]any{"org.matrix.msc3488.ts": now, "org.matrix.msc3488.location": map[string]any{"uri": "geo:1,2"}, "m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$start"}}}
			}
			useCrypto(t, &fakeCrypto{change: map[id.EventID]func(*event.Event){"$point": point}}, nil)
			start := info("$start", true, live)
			first := joined(nil)
			timeline := []map[string]any{}
			if initialState {
				start["origin_server_ts"] = now - 1000
				first = map[string]any{"next_batch": "first", "rooms": map[string]any{"join": map[string]any{"!dm:test": map[string]any{"state": map[string]any{"events": []map[string]any{start}}}}}}
			} else {
				timeline = append(timeline, start)
			}
			// Plaintext coordinates cannot update the tracker, even with a valid reference.
			plain := map[string]any{"type": "org.matrix.msc3672.beacon", "event_id": "$plain", "sender": "@alice:test", "origin_server_ts": live + 1, "content": map[string]any{"org.matrix.msc3488.ts": now, "org.matrix.msc3488.location": map[string]any{"uri": "geo:9,9"}, "m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$start"}}}
			timeline = append(timeline, plain, encryptedEvent("$before", "@alice:test", live+2), encryptedEvent("$point", "@alice:test", live+3), encryptedEvent("$during", "@alice:test", live+4), info("$stop", false, live+5), encryptedEvent("$after", "@alice:test", live+6))
			h := newHomeserver(t, first, joined(map[string][]map[string]any{"!dm:test": timeline}))
			got, _ := runUntilIdle(t, h, testClient(t, h.server.URL), nil)
			if len(got) != 3 {
				t.Fatalf("callbacks: %+v", got)
			}
			if len(got[0].Metadata) != 0 {
				t.Fatal("plaintext coordinates accepted")
			}
			if got[1].Metadata["location"].(map[string]any)["active"] != true {
				t.Fatal("announcement not activated")
			}
			if got[2].Metadata["location"].(map[string]any)["active"] != false {
				t.Fatal("stop ignored")
			}
		})
	}
}

func TestLocationMetadataBounds(t *testing.T) {
	now := time.Now()
	tracker := &locationTracker{}
	point := locationFixture("m.room.message", "$static", now.UnixMilli(), nil)
	point.Content = event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgLocation, GeoURI: "geo:1,2", Body: strings.Repeat("ü", 20000)}}
	tracker.update(point, now)
	description := tracker.snapshot(point.RoomID, point.Sender, now)["location"].(map[string]any)["description"].(string)
	if len(description) > 1024 || !utf8.ValidString(description) {
		t.Fatal("unbounded or invalid description")
	}
	if validGeo("geo:1,2;" + strings.Repeat("x", 1024)) {
		t.Fatal("unbounded URI")
	}
}
