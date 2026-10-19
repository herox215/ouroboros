package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// homeserver is a fake Matrix homeserver for a client whose encryption is
// faked as well. It hands out the scripted syncs one by one and then holds the
// next sync until the client gives up: waiting is closed then, so every
// scripted sync has been handled. A room is a safe room of the bot and Alice
// unless state says otherwise; the state of a broken room cannot be read.
type homeserver struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	user    string // what whoami answers
	device  string
	syncs   []map[string]any
	waiting chan struct{}
	held    sync.Once
	state   map[string][]*event.Event
	broken  map[string]bool
	reads   map[string]int // state requests per room
	joins   []string
	sent    []string // rooms an encrypted message was sent to
	typing  []bool
}

func newHomeserver(t *testing.T, syncs ...map[string]any) *homeserver {
	h := &homeserver{t: t, user: "@bot:test", device: "BOT", syncs: syncs, waiting: make(chan struct{}),
		state: map[string][]*event.Event{}, broken: map[string]bool{}, reads: map[string]int{}}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	return h
}

func (h *homeserver) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.Split(strings.TrimPrefix(r.URL.Path, "/_matrix/client/v3/"), "/")
	encode := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			h.t.Error(err)
		}
	}
	if path[0] == "sync" {
		h.mu.Lock()
		if len(h.syncs) == 0 {
			h.mu.Unlock()
			h.held.Do(func() { close(h.waiting) })
			<-r.Context().Done()
			return
		}
		next := h.syncs[0]
		h.syncs = h.syncs[1:]
		h.mu.Unlock()
		encode(next)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case path[0] == "account" && path[1] == "whoami":
		if h.user == "" {
			w.WriteHeader(http.StatusUnauthorized)
			encode(map[string]string{"errcode": "M_UNKNOWN_TOKEN", "error": "unknown token"})
			return
		}
		encode(map[string]string{"user_id": h.user, "device_id": h.device})
	case path[0] == "user" && path[2] == "filter":
		encode(map[string]string{"filter_id": "fixture-filter"})
	case path[0] == "rooms" && path[2] == "state":
		h.reads[path[1]]++
		if h.broken[path[1]] {
			w.WriteHeader(http.StatusInternalServerError)
			encode(map[string]string{"errcode": "M_UNKNOWN", "error": "state unavailable"})
			return
		}
		state, ok := h.state[path[1]]
		if !ok {
			state = safeEvents()
		}
		encode(state)
	case path[0] == "rooms" && path[2] == "join":
		h.joins = append(h.joins, path[1])
		encode(map[string]string{"room_id": path[1]})
	case path[0] == "rooms" && path[2] == "send":
		if path[3] != "m.room.encrypted" {
			h.t.Errorf("plaintext send attempted: %s", r.URL.Path)
		}
		h.sent = append(h.sent, path[1])
		encode(map[string]string{"event_id": "$sent"})
	case path[0] == "rooms" && path[2] == "typing":
		var body struct{ Typing bool }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			h.t.Error(err)
		}
		h.typing = append(h.typing, body.Typing)
		encode(map[string]any{})
	default:
		h.t.Errorf("unexpected Matrix request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

// room sets the state of a room to the safe state with the members changed.
func (h *homeserver) room(room string, members map[string]event.Membership) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := safeEvents()[:2]
	for user, membership := range members {
		state = append(state, stateEvent(event.StateMember, user, &event.MemberEventContent{Membership: membership}))
	}
	h.state[room] = state
}

func (h *homeserver) breakRoom(room string, broken bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broken[room] = broken
}

func (h *homeserver) requests() (sent, joins []string, typing []bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sent), slices.Clone(h.joins), slices.Clone(h.typing)
}

// encryptedEvent is a Megolm event as a sync carries it; the fake encryption
// does not look at its content.
func encryptedEvent(eventID, sender string, at int64) map[string]any {
	return map[string]any{"type": "m.room.encrypted", "event_id": eventID, "sender": sender, "origin_server_ts": at,
		"content": map[string]any{"algorithm": "m.megolm.v1.aes-sha2", "ciphertext": "opaque", "session_id": "session", "sender_key": "key", "device_id": "DEVICE"}}
}

func memberEvent(user, sender, membership string, at int64) map[string]any {
	return map[string]any{"type": "m.room.member", "event_id": "$member-" + user, "sender": sender, "state_key": user, "origin_server_ts": at,
		"content": map[string]any{"membership": membership}}
}

// joined is a sync with the timelines of joined rooms.
func joined(timelines map[string][]map[string]any) map[string]any {
	rooms := map[string]any{}
	for room, events := range timelines {
		rooms[room] = map[string]any{"timeline": map[string]any{"events": events}}
	}
	return map[string]any{"next_batch": "next", "rooms": map[string]any{"join": rooms}}
}

// fakeCrypto stands in for the encryption. It decrypts an event to a text
// message "text of" its ID from a device of its sender, which change adjusts
// per event, unless failures says that the key for it has not come yet.
type fakeCrypto struct {
	mu        sync.Mutex
	decrypted []id.EventID
	failures  map[id.EventID]int
	change    map[id.EventID]func(*event.Event)
	encrypted int
	plaintext []string     // what was handed to encrypt
	sealing   func() error // runs while a message is encrypted
	closed    bool
}

func (f *fakeCrypto) decrypt(_ context.Context, e *event.Event) (*event.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decrypted = append(f.decrypted, e.ID)
	if f.failures[e.ID] > 0 {
		f.failures[e.ID]--
		return nil, errors.New("no key for the session yet")
	}
	plain := &event.Event{ID: e.ID, RoomID: e.RoomID, Sender: e.Sender, Timestamp: e.Timestamp, Type: event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "text of " + string(e.ID)}}}
	plain.Mautrix.EventSource = e.Mautrix.EventSource | event.SourceDecrypted
	plain.Mautrix.TrustSource = &id.Device{UserID: e.Sender, DeviceID: "DEVICE"}
	if change := f.change[e.ID]; change != nil {
		change(plain)
	}
	return plain, nil
}

func (f *fakeCrypto) encrypt(_ context.Context, _ id.RoomID, _ id.UserID, text string) (*event.EncryptedEventContent, error) {
	f.mu.Lock()
	f.encrypted++
	f.plaintext = append(f.plaintext, text)
	hook := f.sealing
	f.mu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			return nil, err
		}
	}
	return &event.EncryptedEventContent{Algorithm: id.AlgorithmMegolmV1, SessionID: "session", MegolmCiphertext: []byte("opaque")}, nil
}

func (f *fakeCrypto) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeCrypto) tried() []id.EventID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.decrypted)
}

func (f *fakeCrypto) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.plaintext)
}

// useCrypto makes Run start with crypt, or fail with err, instead of setting
// up the encryption; it counts how often Run asks for it.
func useCrypto(t *testing.T, crypt cryptography, err error) *int {
	t.Helper()
	original := startCrypto
	t.Cleanup(func() { startCrypto = original })
	started := new(int)
	startCrypto = func(context.Context, *Client) (cryptography, error) {
		*started++
		return crypt, err
	}
	return started
}

// runUntilIdle runs c until the homeserver has handed out every sync, and
// returns what Run delivered and what it returned.
func runUntilIdle(t *testing.T, h *homeserver, c *Client, onMessage func(context.Context, Message)) ([]Message, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	var delivered []Message
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(ctx context.Context, msg Message) {
			mu.Lock()
			delivered = append(delivered, msg)
			mu.Unlock()
			if onMessage != nil {
				onMessage(ctx, msg)
			}
		})
	}()
	var err error
	select {
	case <-h.waiting:
		cancel()
		err = <-done
	case err = <-done:
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("Run did not get through the scripted syncs")
	}
	mu.Lock()
	defer mu.Unlock()
	return delivered, err
}

func texts(messages []Message) []string {
	var out []string
	for _, msg := range messages {
		out = append(out, msg.Text)
	}
	slices.Sort(out)
	return out
}

func TestRunDeliversOnlyAuthenticatedLiveText(t *testing.T) {
	live, old := time.Now().Add(time.Hour).UnixMilli(), time.Now().Add(-time.Hour).UnixMilli()
	trust := func(state id.TrustState) func(*event.Event) {
		return func(e *event.Event) { e.Mautrix.TrustState = state }
	}
	content := func(change func(*event.MessageEventContent)) func(*event.Event) {
		return func(e *event.Event) { change(e.Content.AsMessage()) }
	}
	crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){
		"$reply": content(func(c *event.MessageEventContent) {
			c.RelatesTo = &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: "$plain"}}
		}),
		"$verified":    trust(id.TrustStateCrossSignedVerified),
		"$unset":       trust(id.TrustStateUnset),
		"$untrusted":   func(e *event.Event) { e.Mautrix.TrustSource = nil },
		"$impostor":    func(e *event.Event) { e.Mautrix.TrustSource.UserID = "@bob:test" },
		"$forwarded":   func(e *event.Event) { e.Mautrix.ForwardedKeys = true },
		"$blacklisted": trust(id.TrustStateBlacklisted),
		"$mismatch":    trust(id.TrustStateDeviceKeyMismatch),
		"$unknown":     trust(id.TrustStateUnknownDevice),
		"$relayed":     trust(id.TrustStateForwarded),
		"$backup":      trust(id.TrustStateBackup),
		"$notice":      content(func(c *event.MessageEventContent) { c.MsgType = event.MsgNotice }),
		"$empty":       content(func(c *event.MessageEventContent) { c.Body = "" }),
		"$edit": content(func(c *event.MessageEventContent) {
			c.RelatesTo = &event.RelatesTo{Type: event.RelReplace, EventID: "$plain"}
		}),
		"$cleartext": func(e *event.Event) { e.Mautrix.EventSource &^= event.SourceDecrypted },
		"$state":     func(e *event.Event) { e.Mautrix.EventSource &^= event.SourceTimeline },
		"$stale":     func(e *event.Event) { e.Timestamp = old },
		"$anonymous": func(e *event.Event) { e.ID = "" },
		"$own": func(e *event.Event) {
			e.Sender = "@bot:test"
			e.Mautrix.TrustSource.UserID = "@bot:test"
		},
		"$disguised": func(e *event.Event) {
			e.Sender = "@mallory:test"
			e.Mautrix.TrustSource.UserID = "@mallory:test"
		},
	}}
	useCrypto(t, crypt, nil)
	var timeline []map[string]any
	for _, name := range []string{"$plain", "$reply", "$verified", "$unset", "$untrusted", "$impostor", "$forwarded", "$blacklisted", "$mismatch", "$unknown", "$relayed", "$backup", "$notice", "$empty", "$edit", "$cleartext", "$state", "$stale", "$anonymous", "$own", "$disguised", "$twice", "$twice"} {
		timeline = append(timeline, encryptedEvent(name, "@alice:test", live))
	}
	timeline = append(timeline,
		encryptedEvent("$stranger", "@mallory:test", live),
		encryptedEvent("$old", "@alice:test", old),
		encryptedEvent("", "@alice:test", live),
		map[string]any{"type": "m.room.message", "event_id": "$plaintext", "sender": "@alice:test", "origin_server_ts": live, "content": map[string]any{"msgtype": "m.text", "body": "not encrypted"}},
	)
	second := joined(map[string][]map[string]any{
		"!dm:test":      timeline,
		"!crowded:test": {encryptedEvent("$crowded", "@alice:test", live)},
		"!bob:test":     {encryptedEvent("$elsewhere", "@alice:test", live)},
		"!broken:test":  {encryptedEvent("$unverifiable", "@alice:test", live)},
	})
	// An encrypted event that is no part of a timeline, and one of a room
	// that was left, are no messages to answer.
	rooms := second["rooms"].(map[string]any)
	rooms["join"].(map[string]any)["!dm:test"].(map[string]any)["state"] = map[string]any{"events": []map[string]any{encryptedEvent("$in-state", "@alice:test", live)}}
	rooms["leave"] = map[string]any{"!left:test": map[string]any{"timeline": map[string]any{"events": []map[string]any{encryptedEvent("$left", "@alice:test", live)}}}}
	// The first sync is history, even where it is newer than the start.
	first := joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$history", "@alice:test", live)}})
	h := newHomeserver(t, first, second)
	h.room("!crowded:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipInvite})
	h.room("!bob:test", map[string]event.Membership{"@bob:test": event.MembershipJoin})
	h.breakRoom("!broken:test", true)
	c := testClient(t, h.server.URL)
	c.allowed["@bob:test"] = true
	c.allowed["@bot:test"] = true
	delivered, err := runUntilIdle(t, h, c, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"text of $plain", "text of $reply", "text of $twice", "text of $unset", "text of $verified"}; !slices.Equal(texts(delivered), want) {
		t.Fatalf("delivered %v, want %v", texts(delivered), want)
	}
	for _, msg := range delivered {
		if msg.RoomID != "!dm:test" || msg.Sender != "@alice:test" {
			t.Fatalf("delivered from the wrong room or sender: %+v", msg)
		}
	}
	for _, skipped := range []id.EventID{"$history", "$stranger", "$old", "", "$in-state", "$left", "$plaintext"} {
		if slices.Contains(crypt.tried(), skipped) {
			t.Errorf("decrypted %q, which is no live message of an allowed sender", skipped)
		}
	}
	// Own and disguised messages are dropped before the room is looked at.
	h.mu.Lock()
	reads := h.reads["!dm:test"]
	h.mu.Unlock()
	if reads != 6 {
		t.Errorf("read the state of the room %d times, want once per accepted message", reads)
	}
}

func TestRunSkipsTimelinesOfNewlyJoinedRooms(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	useCrypto(t, &fakeCrypto{}, nil)
	second := joined(map[string][]map[string]any{
		// The bot just joined: what the timeline shows came before.
		"!new:test": {memberEvent("@bot:test", "@bot:test", "join", live), encryptedEvent("$before-join", "@alice:test", live)},
		// Others joining, or the bot changing its name, is no reason to skip.
		"!dm:test":    {memberEvent("@alice:test", "@alice:test", "join", live), encryptedEvent("$after-alice", "@alice:test", live)},
		"!other:test": {memberEvent("@bot:test", "@bot:test", "leave", live), encryptedEvent("$after-leave", "@alice:test", live)},
		// A member event without a state key joins nobody.
		"!keyless:test": {
			{"type": "m.room.member", "event_id": "$keyless", "sender": "@bot:test", "origin_server_ts": live, "content": map[string]any{"membership": "join"}},
			encryptedEvent("$after-keyless", "@alice:test", live),
		},
	})
	second["rooms"].(map[string]any)["invite"] = map[string]any{
		"!invited:test": map[string]any{"invite_state": map[string]any{"events": []map[string]any{
			{"type": "m.room.encryption", "state_key": "", "sender": "@alice:test", "content": map[string]any{"algorithm": "m.megolm.v1.aes-sha2"}},
			{"type": "m.room.member", "state_key": "@bot:test", "sender": "@alice:test", "content": map[string]any{"membership": "invite"}},
		}}},
	}
	h := newHomeserver(t, joined(nil), second)
	c := testClient(t, h.server.URL)
	delivered, err := runUntilIdle(t, h, c, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"text of $after-alice", "text of $after-keyless", "text of $after-leave"}; !slices.Equal(texts(delivered), want) {
		t.Fatalf("delivered %v, want %v", texts(delivered), want)
	}
	if _, joins, _ := h.requests(); !slices.Equal(joins, []string{"!invited:test"}) {
		t.Fatalf("joined %v, want the room Alice invited to", joins)
	}
}

func TestRunRetriesUndecryptableEventsOnLaterSyncs(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	crypt := &fakeCrypto{failures: map[id.EventID]int{"$late": 1, "$never": 1000}}
	var timeline []map[string]any
	for _, name := range []string{"$late", "$never"} {
		timeline = append(timeline, encryptedEvent(name, "@alice:test", live))
	}
	useCrypto(t, crypt, nil)
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": timeline}), joined(nil), joined(nil))
	c := testClient(t, h.server.URL)
	delivered, err := runUntilIdle(t, h, c, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"text of $late"}; !slices.Equal(texts(delivered), want) {
		t.Fatalf("delivered %v, want %v", texts(delivered), want)
	}
	// $late once when it came and once on the next sync; $never on every sync.
	tries := map[id.EventID]int{}
	for _, e := range crypt.tried() {
		tries[e]++
	}
	if tries["$late"] != 2 || tries["$never"] != 3 {
		t.Fatalf("decryption attempts %v", tries)
	}
}

func TestRunKeepsABoundedNumberOfUndecryptableEvents(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	crypt := &fakeCrypto{failures: map[id.EventID]int{}}
	var timeline []map[string]any
	for i := range 257 {
		name := id.EventID("$event" + strings.Repeat("x", i))
		crypt.failures[name] = 1
		timeline = append(timeline, encryptedEvent(string(name), "@alice:test", live))
	}
	useCrypto(t, crypt, nil)
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": timeline}), joined(nil))
	c := testClient(t, h.server.URL)
	delivered, err := runUntilIdle(t, h, c, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if len(delivered) != 256 {
		t.Fatalf("delivered %d late messages, want the 256 kept for a retry", len(delivered))
	}
}

func TestRunStopsDecryptingWhenCanceled(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	crypt := &fakeCrypto{}
	useCrypto(t, crypt, nil)
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {
		encryptedEvent("$first", "@alice:test", live), encryptedEvent("$second", "@alice:test", live),
	}}), joined(nil))
	c := testClient(t, h.server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var delivered []string
	err := c.Run(ctx, func(_ context.Context, msg Message) {
		delivered = append(delivered, msg.Text)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if !slices.Equal(delivered, []string{"text of $first"}) || !slices.Equal(crypt.tried(), []id.EventID{"$first"}) {
		t.Fatalf("delivered %v after decrypting %v; the rest of the sync must be left alone", delivered, crypt.tried())
	}
}

func TestRunOnlyOnceAndCloseOnlyAfterwards(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	crypt := &fakeCrypto{}
	useCrypto(t, crypt, nil)
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$message", "@alice:test", live)}}))
	c := testClient(t, h.server.URL)
	if c.Status() != "stopped" {
		t.Fatalf("status before Run: %q", c.Status())
	}
	var status string
	var closeErr, runErr error
	delivered, err := runUntilIdle(t, h, c, func(ctx context.Context, _ Message) {
		status = c.Status()
		closeErr = c.Close()
		runErr = c.Run(ctx, nil)
	})
	if !errors.Is(err, context.Canceled) || len(delivered) != 1 {
		t.Fatalf("Run: %v, delivered %v", err, delivered)
	}
	if status != "syncing" || c.Status() != "stopped" {
		t.Fatalf("status while running %q, after %q", status, c.Status())
	}
	if closeErr == nil || runErr == nil {
		t.Fatalf("while running: Close %v, Run %v", closeErr, runErr)
	}
	// The encryption of the first Run stays until Close.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(canceled, nil); err == nil || !strings.Contains(err.Error(), "only Run once") {
		t.Fatalf("second Run: %v", err)
	}
	if err := c.Send(context.Background(), "!dm:test", "after Run"); err == nil {
		t.Fatal("sent after Run")
	}
	if crypt.closed {
		t.Fatal("encryption closed while the client was running")
	}
	if err := c.Close(); err != nil || !crypt.closed {
		t.Fatalf("Close: %v, closed %v", err, crypt.closed)
	}
}

func TestRunRefusesOtherCredentials(t *testing.T) {
	for _, tc := range []struct{ name, user, device string }{
		{"other user", "@eve:test", "BOT"},
		{"other device", "@bot:test", "OTHER"},
		{"unknown token", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := useCrypto(t, &fakeCrypto{}, nil)
			h := newHomeserver(t, joined(nil))
			h.user, h.device = tc.user, tc.device
			c := testClient(t, h.server.URL)
			if err := c.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "do not match") {
				t.Fatalf("Run: %v", err)
			}
			if *started != 0 || c.Status() != "stopped" {
				t.Fatalf("went on with credentials of someone else: crypto started %d times, status %q", *started, c.Status())
			}
		})
	}
}

func TestRunFailsWithoutEncryption(t *testing.T) {
	broken := errors.New("no encryption")
	useCrypto(t, nil, broken)
	h := newHomeserver(t, joined(nil))
	c := testClient(t, h.server.URL)
	if err := c.Run(context.Background(), nil); !errors.Is(err, broken) {
		t.Fatalf("Run: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.syncs) != 1 {
		t.Fatal("synced without encryption")
	}
}

// signingCrypto is a fakeCrypto that cross-signs the device of the client,
// or fails to with err.
type signingCrypto struct {
	fakeCrypto
	err    error
	bounds bool // whether the context of crossSign had a deadline
}

func (s *signingCrypto) crossSign(ctx context.Context, _ *Client) error {
	_, s.bounds = ctx.Deadline()
	return s.err
}

func TestRunTellsWhyTheDeviceIsNotCrossSigned(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"signed", nil, "syncing"},
		{"not signed", errors.New("no keys for this device"), "syncing; no keys for this device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crypt := &signingCrypto{err: tc.err}
			useCrypto(t, crypt, nil)
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$message", "@alice:test", live)}}))
			c := testClient(t, h.server.URL)
			var status string
			delivered, err := runUntilIdle(t, h, c, func(context.Context, Message) { status = c.Status() })
			if !errors.Is(err, context.Canceled) || len(delivered) != 1 {
				t.Fatalf("Run: %v, delivered %v", err, delivered)
			}
			if status != tc.want || !crypt.bounds {
				t.Fatalf("status %q, cross-signing bounded %v", status, crypt.bounds)
			}
		})
	}
}
