package matrix

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// mautrix v0.32 has m.location/GeoURI, but no typed MSC3672 beacon
// content. Decode those extensible event fields locally, without registering
// global SDK content types. Only the authenticated Run handler calls update.
type locationKey struct {
	room   id.RoomID
	sender id.UserID
}
type locationState struct {
	uri, description string
	timestamp        int64
	live, active     bool
	expires, version int64
	beacon           id.EventID
	stateKey         string
	beaconType       string
	started          int64
}
type locationTracker struct {
	mu     sync.Mutex
	states map[locationKey]locationState
}

func beaconInfoType(name string) bool {
	return name == "org.matrix.msc3672.beacon_info" || name == "m.beacon_info" || strings.HasPrefix(name, "org.matrix.msc3672.beacon_info.") || strings.HasPrefix(name, "m.beacon_info.")
}

func locationEvent(e *event.Event) bool {
	switch e.Type.Type {
	case "m.room.message":
		return e.Content.AsMessage().MsgType == event.MsgLocation
	case "org.matrix.msc3672.beacon_info", "org.matrix.msc3672.beacon", "m.beacon_info", "m.beacon":
		return true
	}
	return beaconInfoType(e.Type.Type)
}

// validGeo rejects non-coordinate URIs and non-finite/out-of-range coordinates.
// Altitude and RFC5870 parameters are preserved in the original URI.
func validGeo(uri string) bool {
	if len(uri) > 1024 || !strings.HasPrefix(uri, "geo:") {
		return false
	}
	coords := strings.Split(strings.SplitN(strings.TrimPrefix(uri, "geo:"), ";", 2)[0], ",")
	if len(coords) < 2 || len(coords) > 3 {
		return false
	}
	for i, value := range coords {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || i == 0 && math.Abs(n) > 90 || i == 1 && math.Abs(n) > 180 {
			return false
		}
	}
	return true
}

func (t *locationTracker) update(e *event.Event, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.states == nil {
		t.states = make(map[locationKey]locationState)
	}
	key := locationKey{e.RoomID, e.Sender}
	s := t.states[key]
	// Delayed key retries must not undo a newer update or stop.
	if e.Timestamp < s.version {
		return
	}
	if e.Type.Type == "m.room.message" {
		c := e.Content.AsMessage()
		if c.RelatesTo != nil && c.RelatesTo.Type == event.RelReplace || !validGeo(c.GeoURI) {
			return
		}
		t.states[key] = locationState{uri: c.GeoURI, description: c.Body, timestamp: e.Timestamp, version: e.Timestamp}
		return
	}
	var c struct {
		Live      bool  `json:"live"`
		Timeout   int64 `json:"timeout"`
		Timestamp int64 `json:"org.matrix.msc3488.ts"`
		Location  struct {
			URI         string `json:"uri"`
			Description string `json:"description"`
		} `json:"org.matrix.msc3488.location"`
		Asset struct {
			Type string `json:"type"`
		} `json:"org.matrix.msc3488.asset"`
		Relation struct {
			Type string     `json:"rel_type"`
			ID   id.EventID `json:"event_id"`
		} `json:"m.relates_to"`
	}
	raw, err := json.Marshal(&e.Content)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &fields) != nil {
		return
	}
	// MSC3488 names have stable aliases in Matrix clients.
	for stable, unstable := range map[string]string{"m.ts": "org.matrix.msc3488.ts", "m.location": "org.matrix.msc3488.location", "m.asset": "org.matrix.msc3488.asset"} {
		if value, ok := fields[stable]; ok {
			fields[unstable] = value
		}
	}
	raw, err = json.Marshal(fields)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return
	}
	switch {
	case beaconInfoType(e.Type.Type):
		if e.StateKey != nil && *e.StateKey != string(e.Sender) {
			return
		}
		if c.Asset.Type != "" && c.Asset.Type != "m.self" {
			return
		}
		if !c.Live {
			if s.live && s.beaconType == e.Type.Type && (e.StateKey == nil || s.stateKey == *e.StateKey) {
				s.active = false
				s.version = e.Timestamp
				t.states[key] = s
			}
			return
		}
		// Bound arithmetic and reject already expired or future-dated sessions.
		if c.Timestamp <= 0 || c.Timestamp > now.UnixMilli() || c.Timeout <= 0 || c.Timeout > math.MaxInt64-c.Timestamp || c.Timestamp+c.Timeout <= now.UnixMilli() {
			return
		}
		stateKey := ""
		if e.StateKey != nil {
			stateKey = *e.StateKey
		}
		t.states[key] = locationState{live: true, active: true, expires: c.Timestamp + c.Timeout, version: e.Timestamp, beacon: e.ID, stateKey: stateKey, beaconType: e.Type.Type, started: c.Timestamp}
	case e.Type.Type == "org.matrix.msc3672.beacon" || e.Type.Type == "m.beacon":
		if !s.live || !s.active || s.expires <= now.UnixMilli() || c.Relation.Type != "m.reference" || c.Relation.ID != s.beacon || !validGeo(c.Location.URI) || c.Timestamp < s.started || c.Timestamp > now.UnixMilli() || c.Timestamp >= s.expires || c.Timestamp < s.timestamp {
			return
		}
		s.uri = c.Location.URI
		s.description = c.Location.Description
		s.timestamp = c.Timestamp
		s.version = e.Timestamp
		t.states[key] = s
	}
}

// snapshot allocates every map afresh: receivers may mutate metadata without
// affecting tracking state or any earlier/later message. Times are Unix ms.
func (t *locationTracker) snapshot(room id.RoomID, sender id.UserID, now time.Time) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	metadata := map[string]any{}
	s, ok := t.states[locationKey{room, sender}]
	if !ok || s.uri == "" {
		return metadata
	}
	location := map[string]any{"uri": s.uri, "timestamp": s.timestamp, "live": s.live, "active": s.live && s.active && now.UnixMilli() < s.expires}
	if s.description != "" {
		// Descriptions are optional user text. Bound them so a long caption
		// cannot make every later message exceed the metadata budget.
		description := s.description
		if len(description) > 1024 {
			description = strings.ToValidUTF8(description[:1024], "")
		}
		location["description"] = description
	}
	if s.live {
		location["expires_at"] = s.expires
	}
	metadata["location"] = location
	return metadata
}
