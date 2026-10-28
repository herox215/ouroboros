package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func locationFixture(kind, eventID string, ts int64, raw map[string]any) *event.Event {
	return &event.Event{Type: event.NewEventType(kind), ID: id.EventID(eventID), RoomID: "!dm:test", Sender: "@alice:test", Timestamp: ts, Content: event.Content{Raw: raw}}
}

func TestLocationLiveLifecycleAndIsolation(t *testing.T) {
	now := time.Now()
	ts := now.UnixMilli()
	tracker := &locationTracker{}
	info := locationFixture("org.matrix.msc3672.beacon_info", "$start", ts, map[string]any{
		"live": true, "timeout": int64(10000), "org.matrix.msc3488.ts": ts, "org.matrix.msc3488.asset": map[string]any{"type": "m.self"},
	})
	tracker.update(info, now)
	point := locationFixture("org.matrix.msc3672.beacon", "$point", ts+1, map[string]any{
		"org.matrix.msc3488.ts": ts, "org.matrix.msc3488.location": map[string]any{"uri": "geo:51.5,-0.1", "description": "here"},
		"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$start"},
	})
	// A different sender or room cannot write the current sender's session.
	for _, key := range []locationKey{{"!other:test", "@alice:test"}, {"!dm:test", "@bob:test"}} {
		other := *point
		other.RoomID = key.room
		other.Sender = key.sender
		tracker.update(&other, now)
		if len(tracker.snapshot(key.room, key.sender, now)) != 0 {
			t.Fatal("leaked beacon session")
		}
	}
	wrong := *point
	wrong.Content = event.Content{Raw: map[string]any{"org.matrix.msc3488.ts": ts, "org.matrix.msc3488.location": map[string]any{"uri": "geo:0,0"}, "m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$other"}}}
	tracker.update(&wrong, now)
	if len(tracker.snapshot(info.RoomID, info.Sender, now)) != 0 {
		t.Fatal("accepted unrelated beacon")
	}
	tracker.update(point, now)
	snapshot := tracker.snapshot(info.RoomID, info.Sender, now)
	loc := snapshot["location"].(map[string]any)
	if loc["active"] != true || loc["timestamp"] != ts || loc["uri"] != "geo:51.5,-0.1" {
		t.Fatalf("snapshot: %#v", snapshot)
	}
	loc["uri"] = "mutated"
	if tracker.snapshot(info.RoomID, info.Sender, now)["location"].(map[string]any)["uri"] != "geo:51.5,-0.1" {
		t.Fatal("snapshot aliases tracker")
	}
	if tracker.snapshot(info.RoomID, info.Sender, now.Add(10*time.Second))["location"].(map[string]any)["active"] != false {
		t.Fatal("expired active location")
	}
	stop := locationFixture("org.matrix.msc3672.beacon_info", "$stop", ts+2, map[string]any{"live": false})
	tracker.update(stop, now)
	tracker.update(point, now) // Delayed keys must not undo stop.
	if tracker.snapshot(info.RoomID, info.Sender, now)["location"].(map[string]any)["active"] != false {
		t.Fatal("stop not honored")
	}
	if snapshot["location"].(map[string]any)["active"] != true {
		t.Fatal("earlier snapshot changed")
	}
}

func TestRunLocationMetadataWithoutLocationMessages(t *testing.T) {
	for _, kind := range []event.MessageType{event.MsgText, event.MsgImage, event.MsgFile, event.MsgAudio, event.MsgVideo} {
		t.Run(string(kind), func(t *testing.T) {
			fixture := imageFixture(t)
			media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) }))
			defer media.Close()
			live := time.Now().Add(time.Hour).UnixMilli()
			point := func(e *event.Event) {
				e.Content.Parsed = &event.MessageEventContent{MsgType: event.MsgLocation, Body: "home", GeoURI: "geo:51.5,-0.1"}
			}
			untrusted := func(e *event.Event) { point(e); e.Mautrix.TrustSource = nil }
			text := func(e *event.Event) {
				e.Content.Parsed = &event.MessageEventContent{MsgType: kind, Body: "hello", URL: "mxc://test/media"}
			}
			crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){"$point": point, "$untrusted": untrusted, "$unsafe": point, "$one": text, "$two": text, "$other": text}}
			useCrypto(t, crypt, nil)
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{
				"!dm:test":     {encryptedEvent("$untrusted", "@alice:test", live), encryptedEvent("$point", "@alice:test", live), encryptedEvent("$one", "@alice:test", live), encryptedEvent("$two", "@alice:test", live)},
				"!other:test":  {encryptedEvent("$other", "@alice:test", live)},
				"!unsafe:test": {encryptedEvent("$unsafe", "@alice:test", live)},
			}))
			h.room("!other:test", map[string]event.Membership{"@alice:test": event.MembershipJoin})
			h.room("!unsafe:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipJoin})
			c := testClient(t, h.server.URL)
			c.cli.Client = &http.Client{Transport: mediaTransport{base: http.DefaultTransport, media: media.URL}}
			got, _ := runUntilIdle(t, h, c, func(_ context.Context, m Message) {
				if m.RoomID == "!dm:test" && m.Text != "" && m.Metadata["location"].(map[string]any)["uri"] != "geo:51.5,-0.1" {
					t.Error("missing location")
				}
			})
			if len(got) != 3 {
				t.Fatalf("location triggered callback: %+v", got)
			}
			count := 0
			for _, m := range got {
				if m.RoomID == "!other:test" {
					if len(m.Metadata) != 0 {
						t.Fatal("cross-room metadata")
					}
					continue
				}
				count++
				loc := m.Metadata["location"].(map[string]any)
				if loc["timestamp"] != live || loc["live"] != false || loc["active"] != false {
					t.Fatalf("metadata: %#v", m.Metadata)
				}
				loc["uri"] = "mutated"
			}
			if count != 2 {
				t.Fatal("missing deliveries")
			}
			if crypt.encrypted != 0 {
				t.Fatal("location sent an answer")
			}
		})
	}
}

func TestValidLocationCoordinates(t *testing.T) {
	for _, uri := range []string{"geo:NaN,0", "geo:0,Inf", "https://example.com", "geo:91,0", "geo:0,-181", "geo:1", "geo:1,2,3,4"} {
		if validGeo(uri) {
			t.Errorf("accepted %q", uri)
		}
	}
	for _, uri := range []string{"geo:0,0", "geo:-90,180,4;u=10"} {
		if !validGeo(uri) {
			t.Errorf("rejected %q", uri)
		}
	}
}

func TestRunAuthenticatedLiveBeaconLifecycle(t *testing.T) {
	now := time.Now().UnixMilli()
	live := now + int64(time.Hour/time.Millisecond)
	changes := map[id.EventID]func(*event.Event){}
	beacon := func(kind string, raw map[string]any) func(*event.Event) {
		return func(e *event.Event) { e.Type = event.NewEventType(kind); e.Content = event.Content{Raw: raw} }
	}
	changes["$start"] = beacon("org.matrix.msc3672.beacon_info", map[string]any{"live": true, "timeout": 60000, "org.matrix.msc3488.ts": now})
	changes["$point"] = beacon("org.matrix.msc3672.beacon", map[string]any{
		"org.matrix.msc3488.ts": now, "org.matrix.msc3488.location": map[string]any{"uri": "geo:1,2"},
		"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$start"},
	})
	changes["$stop"] = beacon("org.matrix.msc3672.beacon_info", map[string]any{"live": false})
	changes["$untrusted"] = func(e *event.Event) { changes["$start"](e); e.Mautrix.TrustSource = nil }
	useCrypto(t, &fakeCrypto{change: changes}, nil)
	ids := []string{"$untrusted", "$before", "$start", "$point", "$during", "$stop", "$after"}
	events := make([]map[string]any, 0, len(ids))
	for i, v := range ids {
		events = append(events, encryptedEvent(v, "@alice:test", live+int64(i)))
	}
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": events}))
	got, _ := runUntilIdle(t, h, testClient(t, h.server.URL), nil)
	if len(got) != 3 {
		t.Fatalf("callbacks: %+v", got)
	}
	if len(got[0].Metadata) != 0 {
		t.Fatal("untrusted beacon accepted")
	}
	if got[1].Metadata["location"].(map[string]any)["active"] != true {
		t.Fatal("beacon not active")
	}
	if got[2].Metadata["location"].(map[string]any)["active"] != false {
		t.Fatal("beacon stop ignored")
	}
}

func TestLocationBeaconAliases(t *testing.T) {
	for _, name := range []string{"m.beacon_info", "m.beacon_info.@alice:test.1", "org.matrix.msc3672.beacon_info.@alice:test.1"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			ts := now.UnixMilli()
			tracker := &locationTracker{}
			info := locationFixture(name, "$info", ts, map[string]any{"live": true, "timeout": 1000, "m.ts": ts, "m.asset": map[string]any{"type": "m.self"}})
			key := string(info.Sender)
			info.StateKey = &key
			if !locationEvent(info) {
				t.Fatal("info type not recognized")
			}
			tracker.update(info, now)
			point := locationFixture("m.beacon", "$point", ts, map[string]any{"m.ts": ts, "m.location": map[string]any{"uri": "geo:1,2"}, "m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$info"}})
			tracker.update(point, now)
			loc, ok := tracker.snapshot(info.RoomID, info.Sender, now)["location"].(map[string]any)
			if !ok || loc["active"] != true {
				t.Fatal("stable alias failed")
			}
		})
	}
}

// The tracker ignores what it cannot trust: an edited or invalid location
// message, content it cannot read, a beacon of another user or of no self
// asset, and a session whose times do not hold.
func TestLocationIgnoresWhatItCannotTrust(t *testing.T) {
	now := time.Now()
	ts := now.UnixMilli()
	other := "@bob:test"
	beacon := func(change func(map[string]any)) map[string]any {
		raw := map[string]any{"live": true, "timeout": int64(10000), "org.matrix.msc3488.ts": ts}
		change(raw)
		return raw
	}
	cases := map[string]*event.Event{
		"edited message": {Type: event.EventMessage, RoomID: "!dm:test", Sender: "@alice:test", Timestamp: ts, Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgLocation, GeoURI: "geo:1,2", RelatesTo: &event.RelatesTo{Type: event.RelReplace, EventID: "$old"}}}},
		"invalid message": {Type: event.EventMessage, RoomID: "!dm:test", Sender: "@alice:test", Timestamp: ts, Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgLocation, GeoURI: "geo:91,0"}}},
		"no object":       {Type: event.NewEventType("org.matrix.msc3672.beacon_info"), RoomID: "!dm:test", Sender: "@alice:test", Timestamp: ts, Content: event.Content{VeryRaw: json.RawMessage(`[1]`)}},
		"wrong field":     locationFixture("org.matrix.msc3672.beacon_info", "$wrong", ts, beacon(func(raw map[string]any) { raw["live"] = "yes" })),
		"other self":      locationFixture("org.matrix.msc3672.beacon_info", "$other", ts, beacon(func(map[string]any) {})),
		"not self":        locationFixture("org.matrix.msc3672.beacon_info", "$pet", ts, beacon(func(raw map[string]any) { raw["org.matrix.msc3488.asset"] = map[string]any{"type": "m.pin"} })),
		"no start":        locationFixture("org.matrix.msc3672.beacon_info", "$zero", ts, beacon(func(raw map[string]any) { raw["org.matrix.msc3488.ts"] = 0 })),
	}
	cases["other self"].StateKey = &other
	for name, e := range cases {
		tracker := &locationTracker{}
		tracker.update(e, now)
		if s := tracker.states[locationKey{e.RoomID, e.Sender}]; s != (locationState{}) {
			t.Errorf("%s: tracked %+v", name, s)
		}
	}
}
