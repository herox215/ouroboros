//go:build goolm

package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"ouroboros/internal/home"
	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
)

// matrixServer is a fake homeserver for two clients with real encryption, the
// bot and Alice, in one encrypted room. What one of them sends reaches the
// other with its next sync; the first sync of each is empty, as it is history
// to the client. The token of a client is its user ID.
type matrixServer struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	changed  chan struct{} // closed and replaced when something arrives
	keys     map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys
	otks     map[id.UserID]map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey
	inbox    map[id.UserID][]*event.Event // to-device events
	timeline map[id.UserID][]*event.Event // events of the room
	synced   map[id.UserID]bool
	typing   []bool
	events   int
}

// deviceOf is the one device of each user.
var deviceOf = map[id.UserID]id.DeviceID{"@bot:test": "BOT", "@alice:test": "ALICE"}

func newMatrixServer(t *testing.T) *matrixServer {
	m := &matrixServer{t: t, changed: make(chan struct{}),
		keys: map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys{}, otks: map[id.UserID]map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{},
		inbox: map[id.UserID][]*event.Event{}, timeline: map[id.UserID][]*event.Event{}, synced: map[id.UserID]bool{}}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

// arrived tells waiting syncs that something arrived; m.mu is held.
func (m *matrixServer) arrived() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *matrixServer) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	user := id.UserID(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	encode := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			m.t.Error(err)
		}
	}
	if strings.HasSuffix(r.URL.Path, "/sync") {
		m.sync(r.Context(), user, encode)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/keys/upload"):
		var req mautrix.ReqUploadKeys
		json.NewDecoder(r.Body).Decode(&req)
		device := deviceOf[user]
		if req.DeviceKeys != nil {
			if m.keys[user] == nil {
				m.keys[user] = map[id.DeviceID]mautrix.DeviceKeys{}
			}
			m.keys[user][device] = *req.DeviceKeys
		}
		if m.otks[user] == nil {
			m.otks[user] = map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{device: {}}
		}
		for k, v := range req.OneTimeKeys {
			m.otks[user][device][k] = v
		}
		encode(map[string]any{"one_time_key_counts": map[string]int{"signed_curve25519": len(m.otks[user][device])}})
	case strings.HasSuffix(r.URL.Path, "/keys/query"):
		var req mautrix.ReqQueryKeys
		json.NewDecoder(r.Body).Decode(&req)
		out := map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys{}
		for u := range req.DeviceKeys {
			out[u] = m.keys[u]
		}
		encode(map[string]any{"device_keys": out})
	case strings.HasSuffix(r.URL.Path, "/keys/claim"):
		var req mautrix.ReqClaimKeys
		json.NewDecoder(r.Body).Decode(&req)
		out := map[id.UserID]map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{}
		for u, wanted := range req.OneTimeKeys {
			out[u] = map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{}
			for d := range wanted {
				for k, v := range m.otks[u][d] {
					out[u][d] = map[id.KeyID]mautrix.OneTimeKey{k: v}
					delete(m.otks[u][d], k)
					break
				}
			}
		}
		encode(map[string]any{"one_time_keys": out})
	case strings.Contains(r.URL.Path, "/sendToDevice/"):
		var req mautrix.ReqSendToDevice
		json.NewDecoder(r.Body).Decode(&req)
		parts := strings.Split(r.URL.Path, "/")
		for u, devices := range req.Messages {
			for _, content := range devices {
				m.inbox[u] = append(m.inbox[u], &event.Event{Type: event.NewEventType(parts[len(parts)-2]), Sender: user, Content: *content})
			}
		}
		m.arrived()
		encode(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/state"):
		none := ""
		state := []*event.Event{
			{Type: event.StateEncryption, StateKey: &none, Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}}},
		}
		for member := range deviceOf {
			key := string(member)
			state = append(state, &event.Event{Type: event.StateMember, StateKey: &key, Content: event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipJoin}}})
		}
		encode(state)
	case strings.Contains(r.URL.Path, "/send/"):
		if !strings.Contains(r.URL.Path, "/m.room.encrypted/") {
			m.t.Error("plaintext send attempted")
		}
		var content event.EncryptedEventContent
		json.NewDecoder(r.Body).Decode(&content)
		m.events++
		e := &event.Event{ID: id.EventID(fmt.Sprintf("$event%d", m.events)), Timestamp: time.Now().UnixMilli(), RoomID: "!dm:test", Sender: user, Type: event.EventEncrypted, Content: event.Content{Parsed: &content}}
		for member := range deviceOf {
			if member != user {
				m.timeline[member] = append(m.timeline[member], e)
			}
		}
		m.arrived()
		encode(map[string]string{"event_id": string(e.ID)})
	case strings.Contains(r.URL.Path, "/typing/"):
		var body struct{ Typing bool }
		json.NewDecoder(r.Body).Decode(&body)
		if user != "@bot:test" || !strings.HasSuffix(r.URL.Path, "/rooms/!dm:test/typing/@bot:test") {
			m.t.Errorf("unexpected typing %s of %s", r.URL.Path, user)
		}
		m.typing = append(m.typing, body.Typing)
		encode(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/keys/device_signing/upload"), strings.Contains(r.URL.Path, "/account_data/"):
		// The cross-signing keys of a client and its secret storage are taken
		// and forgotten: each start gives the client new ones.
		encode(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/keys/signatures/upload"):
		encode(map[string]any{"failures": map[string]any{}})
	case strings.Contains(r.URL.Path, "/filter"):
		encode(map[string]string{"filter_id": "fixture-filter"})
	case strings.HasSuffix(r.URL.Path, "/whoami"):
		encode(map[string]string{"user_id": string(user), "device_id": string(deviceOf[user])})
	default:
		m.t.Errorf("unexpected Matrix request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

// sync answers the sync of user with what arrived for it, once something did.
func (m *matrixServer) sync(ctx context.Context, user id.UserID, encode func(any)) {
	m.mu.Lock()
	first := !m.synced[user]
	m.synced[user] = true
	for !first && len(m.inbox[user])+len(m.timeline[user]) == 0 {
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return
		}
		m.mu.Lock()
	}
	inbox, timeline := m.inbox[user], m.timeline[user]
	if first {
		inbox, timeline = nil, nil
	}
	delete(m.inbox, user)
	if !first {
		delete(m.timeline, user)
	}
	m.mu.Unlock()
	encode(map[string]any{"next_batch": "next", "device_one_time_keys_count": map[string]int{"signed_curve25519": 50},
		"to_device": map[string]any{"events": inbox}, "rooms": map[string]any{"join": map[string]any{"!dm:test": map[string]any{"timeline": map[string]any{"events": timeline}}}}})
}

func (m *matrixServer) typed() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.typing)
}

// eventually waits until done reports true.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !done(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestGatewayAnswersAnEncryptedMessage(t *testing.T) {
	m := newMatrixServer(t)
	alice, err := transport.New(transport.Config{Homeserver: m.server.URL, UserID: "@alice:test", Token: "@alice:test", DeviceID: "ALICE", StateDir: t.TempDir(), AllowedUsers: []string{"@bot:test"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stopAlice := context.WithTimeout(context.Background(), time.Minute)
	answers := make(chan transport.Message, 4)
	aliceDone := make(chan error, 1)
	go func() {
		aliceDone <- alice.Run(ctx, func(_ context.Context, msg transport.Message) { answers <- msg })
	}()
	defer func() {
		stopAlice()
		<-aliceDone
		alice.Close()
	}()

	// The gateway of the owner is logged in as the bot and allows Alice.
	dir := home.Dir(t.TempDir())
	s := store{filepath.Join(dir.Gateways(), "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(configuration{Enabled: true, Homeserver: m.server.URL, UserID: "@bot:test", Token: "@bot:test", DeviceID: "BOT", AllowedUsers: []string{"@alice:test"}, Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	sessions := &testSessions{asked: make(chan string, 4)}
	_, serving := startPlugin(t, dir, sessions)
	serving.Start()
	defer serving.Stop()
	waitStatus(t, s, "1", "syncing")
	eventually(t, "Alice to sync", func() bool { return alice.Status() == "syncing" })

	if err := alice.SendTo(ctx, "!dm:test", "@bot:test", "hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-sessions.asked:
		if text != "hello" {
			t.Fatalf("asked %q", text)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the message did not reach the session")
	}
	// While the thread works, Alice sees the bot typing.
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionMessage, Text: "hello"}, plugin.SessionEvent{Kind: plugin.SessionWorking, N: 1})
	eventually(t, "typing", func() bool { return slices.Equal(m.typed(), []bool{true}) })
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionMessage, Text: "hello"}, plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "pong"})
	select {
	case msg := <-answers:
		if msg.Text != "pong" || msg.Sender != "@bot:test" || msg.RoomID != "!dm:test" {
			t.Fatalf("Alice got %+v", msg)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the answer did not reach Alice")
	}
	if typed := m.typed(); !slices.Equal(typed, []bool{true, false}) {
		t.Fatalf("typing %v", typed)
	}
	serving.Stop()
	waitStatus(t, s, "1", "Stopped")
}
