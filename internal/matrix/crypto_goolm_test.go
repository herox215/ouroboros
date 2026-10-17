//go:build goolm

package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
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
	// failQuery makes the homeserver fail to tell the devices of users.
	failQuery bool
	sync      func(http.ResponseWriter, *http.Request)
	// The cross-signing keys of each user, how often they and signatures were
	// uploaded, and the account data of each user.
	crossSigning map[id.UserID]mautrix.UploadCrossSigningKeysReq[any]
	uploads      int
	signatures   int
	accountData  map[id.UserID]map[string]json.RawMessage
	// refuse, when set, makes the homeserver fail the requests it reports.
	refuse func(*http.Request) bool
}

func newMock(t *testing.T) *mockMatrix {
	m := &mockMatrix{t: t, keys: map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys{}, otks: map[id.UserID]map[id.DeviceID]map[id.KeyID]mautrix.OneTimeKey{}, inbox: map[id.UserID][]*event.Event{},
		crossSigning: map[id.UserID]mautrix.UploadCrossSigningKeysReq[any]{}, accountData: map[id.UserID]map[string]json.RawMessage{}}
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
	case m.refuse != nil && m.refuse(r):
		w.WriteHeader(http.StatusInternalServerError)
		encode(map[string]string{"errcode": "M_UNKNOWN", "error": "refused"})
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
	case strings.HasSuffix(r.URL.Path, "/keys/query") && m.failQuery:
		w.WriteHeader(http.StatusInternalServerError)
		encode(map[string]string{"errcode": "M_UNKNOWN", "error": "device lists unavailable"})
	case strings.HasSuffix(r.URL.Path, "/keys/query"):
		var req mautrix.ReqQueryKeys
		json.NewDecoder(r.Body).Decode(&req)
		out := map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys{}
		master := map[id.UserID]mautrix.CrossSigningKeys{}
		self := map[id.UserID]mautrix.CrossSigningKeys{}
		for u := range req.DeviceKeys {
			out[u] = m.keys[u]
			if keys, ok := m.crossSigning[u]; ok {
				master[u], self[u] = keys.Master, keys.SelfSigning
			}
		}
		encode(map[string]any{"device_keys": out, "master_keys": master, "self_signing_keys": self})
	case strings.HasSuffix(r.URL.Path, "/keys/device_signing/upload"):
		var req mautrix.UploadCrossSigningKeysReq[any]
		json.NewDecoder(r.Body).Decode(&req)
		m.crossSigning[user] = req
		m.uploads++
		encode(map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/keys/signatures/upload"):
		// A signature is added to the device or the master key it names.
		var req mautrix.ReqUploadSignatures
		json.NewDecoder(r.Body).Decode(&req)
		m.signatures++
		for u, signed := range req {
			for name, sig := range signed {
				if device, ok := m.keys[u][id.DeviceID(name)]; ok {
					device.Signatures = merged(device.Signatures, sig.Signatures)
					m.keys[u][id.DeviceID(name)] = device
				} else if keys := m.crossSigning[u]; keys.Master.FirstKey().String() == name {
					keys.Master.Signatures = merged(keys.Master.Signatures, sig.Signatures)
					m.crossSigning[u] = keys
				} else {
					m.t.Errorf("signature of unknown key %s of %s", name, u)
				}
			}
		}
		encode(map[string]any{"failures": map[string]any{}})
	case strings.Contains(r.URL.Path, "/account_data/"):
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if r.Method == http.MethodPut {
			if m.accountData[user] == nil {
				m.accountData[user] = map[string]json.RawMessage{}
			}
			var data json.RawMessage
			json.NewDecoder(r.Body).Decode(&data)
			m.accountData[user][name] = data
			encode(map[string]any{})
		} else if data, ok := m.accountData[user][name]; ok {
			w.Write(data)
		} else {
			w.WriteHeader(http.StatusNotFound)
			encode(map[string]string{"errcode": "M_NOT_FOUND", "error": "no account data"})
		}
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

// merged is signatures with those of more added.
func merged(signatures, more map[id.UserID]map[id.KeyID]string) map[id.UserID]map[id.KeyID]string {
	if signatures == nil {
		signatures = map[id.UserID]map[id.KeyID]string{}
	}
	for u, keys := range more {
		if signatures[u] == nil {
			signatures[u] = map[id.KeyID]string{}
		}
		for k, v := range keys {
			signatures[u][k] = v
		}
	}
	return signatures
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
			// Both syncs are handled.
			cancel()
			<-r.Context().Done()
			return
		}
		encrypted, err := aliceCrypto.encrypt(ctx, "!dm:test", "@bot:test", "live secret")
		if err != nil {
			t.Error(err)
			cancel()
			return
		}
		// A room that was left is no room to answer in, whatever its state.
		left, err := aliceCrypto.encrypt(ctx, "!left:test", "@bot:test", "left secret")
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
		gone := &event.Event{ID: id.EventID(fmt.Sprintf("$left%d", n)), Timestamp: time.Now().UnixMilli(), Sender: "@alice:test", Type: event.EventEncrypted, Content: event.Content{Parsed: left}}
		plain := &event.Event{ID: "$plaintext", Timestamp: time.Now().UnixMilli(), Sender: "@alice:test", Type: event.EventMessage, Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "not allowed"}}}
		json.NewEncoder(w).Encode(map[string]any{"next_batch": fmt.Sprintf("batch%d", n), "device_one_time_keys_count": map[string]int{"signed_curve25519": 50}, "to_device": map[string]any{"events": toDevice}, "rooms": map[string]any{
			"join":  map[string]any{"!dm:test": map[string]any{"state": map[string]any{"events": safeEvents()}, "timeline": map[string]any{"events": []*event.Event{e, plain}}}},
			"leave": map[string]any{"!left:test": map[string]any{"timeline": map[string]any{"events": []*event.Event{gone}}}},
		}})
	}
	received := []Message{}
	err := bot.Run(ctx, func(ctx context.Context, msg Message) { received = append(received, msg) })
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

// refuseChmod makes changing the mode of path fail until the returned
// function puts chmod back.
func refuseChmod(t *testing.T, path string) func() {
	original := chmod
	chmod = func(name string, mode os.FileMode) error {
		if name == path {
			return &os.PathError{Op: "chmod", Path: name, Err: syscall.EPERM}
		}
		return original(name, mode)
	}
	restore := func() { chmod = original }
	t.Cleanup(restore)
	return restore
}

func TestNewCryptoRefusesUnusableState(t *testing.T) {
	file := func(t *testing.T, path string, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	remove := func(path string) func() { return func() { os.RemoveAll(path) } }
	for _, tc := range []struct {
		name string
		// prepare gets in the way in the crypto directory dir of c, and
		// returns what clears the way again, or nil.
		prepare func(t *testing.T, c *Client, dir string) func()
		want    string
	}{
		{"state directory below a file", func(t *testing.T, c *Client, dir string) func() {
			file(t, filepath.Join(c.cfg.StateDir, "file"), "")
			c.cfg.StateDir = filepath.Join(c.cfg.StateDir, "file", "state")
			return nil
		}, "not a directory"},
		{"state directory keeps its mode", func(t *testing.T, c *Client, dir string) func() {
			return refuseChmod(t, c.cfg.StateDir)
		}, "operation not permitted"},
		{"crypto directory is a file", func(t *testing.T, c *Client, dir string) func() {
			file(t, dir, "")
			return remove(dir)
		}, "not a directory"},
		{"crypto directory keeps its mode", func(t *testing.T, c *Client, dir string) func() {
			return refuseChmod(t, dir)
		}, "operation not permitted"},
		{"lock is a directory", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(filepath.Join(dir, "crypto.lock"), 0700)
			return remove(filepath.Join(dir, "crypto.lock"))
		}, "is a directory"},
		{"key is a directory", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(filepath.Join(dir, "pickle.key"), 0700)
			return remove(filepath.Join(dir, "pickle.key"))
		}, "is a directory"},
		{"key is a dangling link", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(dir, 0700)
			if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "pickle.key")); err != nil {
				t.Fatal(err)
			}
			return func() {
				if _, err := os.Lstat(filepath.Join(dir, "elsewhere")); err == nil {
					t.Error("wrote the key through a link")
				}
				os.Remove(filepath.Join(dir, "pickle.key"))
			}
		}, "file exists"},
		{"key is too short", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(dir, 0700)
			file(t, filepath.Join(dir, "pickle.key"), "short")
			return remove(filepath.Join(dir, "pickle.key"))
		}, "invalid Matrix crypto pickle key"},
		{"key is too long", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(dir, 0700)
			file(t, filepath.Join(dir, "pickle.key"), strings.Repeat("k", 33))
			return remove(filepath.Join(dir, "pickle.key"))
		}, "invalid Matrix crypto pickle key"},
		{"key keeps its mode", func(t *testing.T, c *Client, dir string) func() {
			return refuseChmod(t, filepath.Join(dir, "pickle.key"))
		}, "operation not permitted"},
		{"database is a directory", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(filepath.Join(dir, "crypto.db"), 0700)
			return remove(filepath.Join(dir, "crypto.db"))
		}, "is a directory"},
		{"database keeps its mode", func(t *testing.T, c *Client, dir string) func() {
			return refuseChmod(t, filepath.Join(dir, "crypto.db"))
		}, "operation not permitted"},
		{"database is no database", func(t *testing.T, c *Client, dir string) func() {
			os.MkdirAll(dir, 0700)
			file(t, filepath.Join(dir, "crypto.db"), strings.Repeat("no database ", 100))
			return remove(filepath.Join(dir, "crypto.db"))
		}, "initialize Matrix crypto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMock(t)
			c := m.client(t, "@bot:test", "BOT")
			repair := tc.prepare(t, c, cryptoStateDir(c.cfg))
			crypt, err := newCrypto(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
			if crypt != nil {
				t.Fatal("returned encryption with the error")
			}
			if repair == nil {
				return
			}
			// Nothing of the failed start, not even the lock, is in the way
			// of the next one.
			repair()
			setupCrypto(t, c)
		})
	}
}

func TestNewCryptoKeepsItsFilesPrivate(t *testing.T) {
	m := newMock(t)
	c := m.client(t, "@bot:test", "BOT")
	dir := cryptoStateDir(c.cfg)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	for path, data := range map[string][]byte{"pickle.key": key, "crypto.db": nil} {
		if err := os.WriteFile(filepath.Join(dir, path), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{c.cfg.StateDir, dir} {
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	setupCrypto(t, c)
	for path, mode := range map[string]os.FileMode{c.cfg.StateDir: 0700, dir: 0700, filepath.Join(dir, "pickle.key"): 0600, filepath.Join(dir, "crypto.db"): 0600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v %v, want mode %v", path, info.Mode().Perm(), err, mode)
		}
	}
	if kept, err := os.ReadFile(filepath.Join(dir, "pickle.key")); err != nil || string(kept) != string(key) {
		t.Fatalf("replaced the key: %q %v", kept, err)
	}
}

func TestNewCryptoNeedsTheKeyOnDisk(t *testing.T) {
	// RLIMIT_FSIZE is process-wide: isolate it from Go's asynchronous test-cache
	// log writes, which otherwise permanently fail with EFBIG during this test.
	if os.Getenv("OURO_MATRIX_KEY_WRITE_FIXTURE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewCryptoNeedsTheKeyOnDisk$")
		cmd.Env = append(os.Environ(), "OURO_MATRIX_KEY_WRITE_FIXTURE=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("key-write fixture failed: %v\n%s", err, output)
		}
		return
	}
	m := newMock(t)
	c := m.client(t, "@bot:test", "BOT")
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	// While this holds, no file of the process can grow: the key cannot be
	// written, as on a full disk.
	none := limit
	none.Cur = 0
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &none); err != nil {
		t.Fatal(err)
	}
	crypt, err := newCrypto(context.Background(), c)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(err, syscall.EFBIG) || crypt != nil {
		t.Fatalf("started with a key that is not on disk: %v", err)
	}
	// No empty key is left in the way of the next start.
	if _, err := os.Lstat(filepath.Join(cryptoStateDir(c.cfg), "pickle.key")); !os.IsNotExist(err) {
		t.Fatalf("left the key behind: %v", err)
	}
	setupCrypto(t, c)
}

func TestSDKEncryptionFailuresSendNothing(t *testing.T) {
	m := newMock(t)
	bot := m.client(t, "@bot:test", "BOT")
	botCrypto := setupCrypto(t, bot)
	bot.running = true
	// Nobody knows the devices of Alice when the homeserver cannot tell them.
	m.mu.Lock()
	m.failQuery = true
	m.mu.Unlock()
	if err := bot.Send(context.Background(), "!dm:test", "must not leave"); err == nil {
		t.Fatal("sent without the devices of the peer")
	}
	m.mu.Lock()
	m.failQuery = false
	m.mu.Unlock()
	// Without its store, the encryption cannot start a new session.
	botCrypto.helper.Close()
	if _, err := botCrypto.encrypt(context.Background(), "!dm:test", "@alice:test", "must not leave"); err == nil {
		t.Fatal("encrypted without a session store")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) != 0 || len(m.inbox) != 0 {
		t.Fatalf("leaked %d messages and %d key shares", len(m.sent), len(m.inbox))
	}
}

func TestSDKCloseReportsTheLockItCouldNotRelease(t *testing.T) {
	m := newMock(t)
	crypt, err := newCrypto(context.Background(), m.client(t, "@bot:test", "BOT"))
	if err != nil {
		t.Fatal(err)
	}
	crypt.(*sdkCrypto).lock.Close()
	if err := crypt.close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close: %v", err)
	}
}

func TestSDKRefusesToForwardRoomKeys(t *testing.T) {
	m := newMock(t)
	bot := m.client(t, "@bot:test", "BOT")
	alice := m.client(t, "@alice:test", "ALICE")
	botCrypto := setupCrypto(t, bot)
	setupCrypto(t, alice)
	bot.running = true
	if err := bot.Send(context.Background(), "!dm:test", "secret reply"); err != nil {
		t.Fatal(err)
	}
	shares := func() int {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.inbox["@alice:test"])
	}
	m.mu.Lock()
	session := m.sent[0].Content.AsEncrypted().SessionID
	m.mu.Unlock()
	shared := shares()
	request := &event.RoomKeyRequestEventContent{Action: event.KeyRequestActionRequest, RequestingDeviceID: "ALICE", RequestID: "request",
		Body: event.RequestedKeyInfo{Algorithm: id.AlgorithmMegolmV1, RoomID: "!dm:test", SessionID: session}}
	mach := botCrypto.helper.Machine()
	mach.HandleRoomKeyRequest(context.Background(), "@alice:test", request)
	if shares() != shared {
		t.Fatal("answered a request for a room key")
	}
	// Ensure the refusal did not just reflect a broken fixture: a policy
	// that allows it forwards the key.
	mach.AllowKeyShare = func(context.Context, *id.Device, event.RequestedKeyInfo) *crypto.KeyShareRejection { return nil }
	mach.HandleRoomKeyRequest(context.Background(), "@alice:test", request)
	if shares() == shared {
		t.Fatal("the fixture cannot forward keys")
	}
}
