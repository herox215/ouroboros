//go:build goolm

package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type mockMatrix struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	keys   map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys
	otks   map[id.UserID]map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey
	inbox  map[id.UserID][]*event.Event
	sent   []*event.Event
	extra  bool
	sync   func(http.ResponseWriter, *http.Request)
}

func newMock(t *testing.T) *mockMatrix {
	m := &mockMatrix{t: t, keys: map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys{}, otks: map[id.UserID]map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{}, inbox: map[id.UserID][]*event.Event{}}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}
func (m *mockMatrix) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	user := id.UserID(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if strings.HasSuffix(r.URL.Path, "/sync") && m.sync != nil {
		m.sync(w, r)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	encode := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			m.t.Error(err)
		}
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/keys/upload"):
		var req mautrix.ReqUploadKeys
		json.NewDecoder(r.Body).Decode(&req)
		if req.DeviceKeys != nil {
			if m.keys[user] == nil {
				m.keys[user] = map[id.DeviceID]mautrix.DeviceKeys{}
			}
			m.keys[user][req.DeviceKeys.DeviceID] = *req.DeviceKeys
		}
		var device id.DeviceID
		for d := range m.keys[user] {
			device = d
		}
		if m.otks[user] == nil {
			m.otks[user] = map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{}
		}
		if m.otks[user][device] == nil {
			m.otks[user][device] = map[id.KeyID]mautrix.OneTimeKey{}
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
		for u, devices := range req.OneTimeKeys {
			out[u] = map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{}
			for d := range devices {
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
		typ := parts[len(parts)-2]
		for u, devices := range req.Messages {
			for _, content := range devices {
				m.inbox[u] = append(m.inbox[u], &event.Event{Type: event.NewEventType(typ), Sender: user, Content: *content})
			}
		}
		encode(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/state"):
		state := safeEvents()
		if m.extra {
			state = append(state, stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipInvite}))
		}
		encode(state)
	case strings.Contains(r.URL.Path, "/send/"):
		if !strings.Contains(r.URL.Path, "/m.room.encrypted/") {
			m.t.Error("plaintext send attempted")
		}
		var content event.EncryptedEventContent
		json.NewDecoder(r.Body).Decode(&content)
		m.sent = append(m.sent, &event.Event{ID: id.EventID(fmt.Sprintf("$sent%d", len(m.sent))), Timestamp: time.Now().UnixMilli(), RoomID: "!dm:test", Sender: user, Type: event.EventEncrypted, Content: event.Content{Parsed: &content}})
		encode(map[string]string{"event_id": "$sent"})
	case strings.Contains(r.URL.Path, "/filter"):
		encode(map[string]string{"filter_id": "fixture-filter"})
	case strings.HasSuffix(r.URL.Path, "/whoami"):
		device := "BOT"
		if user == "@alice:test" {
			device = "ALICE"
		}
		encode(map[string]string{"user_id": string(user), "device_id": device})
	default:
		m.t.Errorf("unexpected Matrix request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", 404)
	}
}
func (m *mockMatrix) client(t *testing.T, user, device string) *Client {
	t.Helper()
	c, err := New(Config{Homeserver: m.server.URL, UserID: user, Token: user, DeviceID: device, StateDir: t.TempDir(), AllowedUsers: []string{"@alice:test", "@bot:test"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func setupCrypto(t *testing.T, c *Client) *sdkCrypto {
	t.Helper()
	crypt, err := newCrypto(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.crypto = crypt
	t.Cleanup(func() { c.running = false; c.Close() })
	return crypt.(*sdkCrypto)
}
func (m *mockMatrix) deliver(to *Client) {
	m.mu.Lock()
	events := m.inbox[to.cli.UserID]
	delete(m.inbox, to.cli.UserID)
	m.mu.Unlock()
	resp := &mautrix.RespSync{}
	resp.ToDevice.Events = events
	to.crypto.(*sdkCrypto).helper.Machine().ProcessSyncResponse(context.Background(), resp, "")
}
func TestSDKEncryptedReplyAndPersistence(t *testing.T) {
	m := newMock(t)
	bot := m.client(t, "@bot:test", "BOT")
	alice := m.client(t, "@alice:test", "ALICE")
	botCrypto := setupCrypto(t, bot)
	aliceCrypto := setupCrypto(t, alice)
	identity := botCrypto.helper.Machine().OwnIdentity().IdentityKey
	duplicate, err := New(bot.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newCrypto(context.Background(), duplicate); err == nil {
		t.Fatal("concurrent crypto state use was permitted")
	}
	bot.running = true
	if err := bot.Send(context.Background(), "!dm:test", "secret reply"); err != nil {
		t.Fatal(err)
	}
	m.deliver(alice)
	m.mu.Lock()
	if len(m.sent) != 1 {
		t.Fatal("expected encrypted send")
	}
	encrypted := m.sent[0]
	m.mu.Unlock()
	plain, err := aliceCrypto.decrypt(context.Background(), encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Content.AsMessage().Body != "secret reply" {
		t.Fatalf("bad roundtrip: %#v", plain.Content)
	}
	if plain.Mautrix.TrustSource == nil || plain.Mautrix.TrustSource.UserID != "@bot:test" {
		t.Fatal("sender not authenticated")
	}
	if encrypted.Content.AsEncrypted().Algorithm != id.AlgorithmMegolmV1 {
		t.Fatal("wrong wire algorithm")
	}
	m.mu.Lock()
	m.extra = true
	m.mu.Unlock()
	if err := bot.Send(context.Background(), "!dm:test", "must not leave"); err == nil {
		t.Fatal("unsafe room accepted")
	}
	m.mu.Lock()
	if len(m.sent) != 1 {
		t.Fatal("sent to unsafe room")
	}
	m.extra = false
	m.mu.Unlock()
	bot.running = false
	if err := bot.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := New(bot.cfg)
	if err != nil {
		t.Fatal(err)
	}
	resumedCrypto := setupCrypto(t, resumed)
	if resumedCrypto.helper.Machine().OwnIdentity().IdentityKey != identity {
		t.Fatal("crypto identity lost across restart")
	}
	for _, name := range []string{"crypto.db", "pickle.key"} {
		info, err := os.Stat(filepath.Join(cryptoStateDir(bot.cfg), name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe file permissions %s: %v", name, err)
		}
	}
}

func TestSDKSendToRejectsReplacedPeerBeforeKeySharing(t *testing.T) {
	m := newMock(t)
	bot := m.client(t, "@bot:test", "BOT")
	// Both users are allowed, but the room currently contains Alice, not the
	// original sender Bob. Alice has real device keys to receive leaked keys.
	bot.allowed["@bob:test"] = true
	alice := m.client(t, "@alice:test", "ALICE")
	setupCrypto(t, bot)
	setupCrypto(t, alice)
	bot.running = true
	for _, expected := range []string{"@bob:test", ""} {
		if err := bot.SendTo(context.Background(), "!dm:test", expected, "queued secret for Bob"); err == nil {
			t.Fatalf("accepted unexpected peer %q", expected)
		}
	}
	m.mu.Lock()
	sent := len(m.sent)
	shared := 0
	for _, inbox := range m.inbox {
		shared += len(inbox)
	}
	m.mu.Unlock()
	if sent != 0 || shared != 0 {
		t.Fatalf("rejected reply leaked: %d encrypted sends, %d to-device events", sent, shared)
	}
	// Ensure rejection did not just reflect a broken SDK fixture: the matching
	// peer must receive both a key share and an encrypted message.
	if err := bot.SendTo(context.Background(), "!dm:test", "@alice:test", "reply for Alice"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) != 1 || len(m.inbox["@alice:test"]) == 0 {
		t.Fatal("matching peer did not receive encrypted send and key share")
	}
}

func TestRunSyncDropsHistoryAndPlaintext(t *testing.T) {
	m := newMock(t)
	alice := m.client(t, "@alice:test", "ALICE")
	aliceCrypto := setupCrypto(t, alice)
	bot := m.client(t, "@bot:test", "BOT")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := 0
	m.sync = func(w http.ResponseWriter, r *http.Request) {
		n++
		if n > 2 {
			<-r.Context().Done()
			return
		}
		encrypted, err := aliceCrypto.encrypt(ctx, "!dm:test", "@bot:test", "live secret")
		if err != nil {
			t.Error(err)
			cancel()
			return
		}
		m.mu.Lock()
		toDevice := m.inbox["@bot:test"]
		delete(m.inbox, "@bot:test")
		m.mu.Unlock()
		e := &event.Event{ID: id.EventID(fmt.Sprintf("$event%d", n)), Timestamp: time.Now().UnixMilli(), Sender: "@alice:test", Type: event.EventEncrypted, Content: event.Content{Parsed: encrypted}}
		plain := &event.Event{ID: "$plaintext", Timestamp: time.Now().UnixMilli(), Sender: "@alice:test", Type: event.EventMessage, Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "not allowed"}}}
		json.NewEncoder(w).Encode(map[string]any{"next_batch": fmt.Sprintf("batch%d", n), "device_one_time_keys_count": map[string]int{"signed_curve25519": 50}, "to_device": map[string]any{"events": toDevice}, "rooms": map[string]any{"join": map[string]any{"!dm:test": map[string]any{"state": map[string]any{"events": safeEvents()}, "timeline": map[string]any{"events": []*event.Event{e, plain}}}}}})
	}
	received := []Message{}
	err := bot.Run(ctx, func(ctx context.Context, msg Message) { received = append(received, msg); cancel() })
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	if len(received) != 1 || received[0].Text != "live secret" || received[0].Sender != "@alice:test" {
		t.Fatalf("wrong deliveries: %#v", received)
	}
	if n < 2 {
		t.Fatal("did not advance sync")
	}
	if err := bot.Close(); err != nil {
		t.Fatal(err)
	}
}
