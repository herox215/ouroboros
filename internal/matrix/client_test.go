package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func stateEvent(t event.Type, key string, content any) *event.Event {
	return &event.Event{Type: t, StateKey: &key, Content: event.Content{Parsed: content}}
}
func safeEvents() []*event.Event {
	return []*event.Event{
		stateEvent(event.StateEncryption, "", &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}),
		stateEvent(event.StateMember, "@bot:test", &event.MemberEventContent{Membership: event.MembershipJoin}),
		stateEvent(event.StateMember, "@alice:test", &event.MemberEventContent{Membership: event.MembershipJoin}),
	}
}
func testClient(t *testing.T, server string) *Client {
	t.Helper()
	c, err := New(Config{Homeserver: server, UserID: "@bot:test", Token: "fixture-token", DeviceID: "BOT", StateDir: t.TempDir(), AllowedUsers: []string{"@alice:test"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/_matrix/client/v3/login" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var req mautrix.ReqLogin
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Type != mautrix.AuthTypePassword || req.Identifier.User != "@bot:test" || req.Password != "fixture-password" {
			t.Errorf("bad login request")
		}
		json.NewEncoder(w).Encode(map[string]any{"user_id": "@bot:test", "access_token": "fixture-token", "device_id": "BOT"})
	}))
	defer server.Close()
	cred, err := Login(context.Background(), server.URL, "@bot:test", "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Token != "fixture-token" || cred.UserID != "@bot:test" || cred.DeviceID != "BOT" || cred.Homeserver != server.URL {
		t.Fatalf("bad credentials: %#v", cred)
	}
}
func TestLoginFailures(t *testing.T) {
	var answer map[string]any
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(answer)
	}))
	defer server.Close()
	if _, err := Login(context.Background(), "http://matrix.example", "@bot:test", "fixture-password"); err == nil {
		t.Fatal("logged in over plain HTTP to another host")
	}
	// A homeserver can echo the request: its error must not reach the owner.
	status, answer = http.StatusForbidden, map[string]any{"errcode": "M_FORBIDDEN", "error": "wrong password fixture-password"}
	if _, err := Login(context.Background(), server.URL, "@bot:test", "fixture-password"); err == nil || strings.Contains(err.Error(), "fixture-password") {
		t.Fatalf("failed login: %v", err)
	}
	status = http.StatusOK
	for _, missing := range []string{"user_id", "access_token", "device_id"} {
		answer = map[string]any{"user_id": "@bot:test", "access_token": "fixture-token", "device_id": "BOT"}
		delete(answer, missing)
		if cred, err := Login(context.Background(), server.URL, "@bot:test", "fixture-password"); err == nil {
			t.Fatalf("accepted a login without %s: %+v", missing, cred)
		}
	}
}
func TestNewRejectsIncompleteConfig(t *testing.T) {
	valid := Config{Homeserver: "https://matrix.test", UserID: "@bot:test", Token: "fixture-token", DeviceID: "BOT", StateDir: t.TempDir(), AllowedUsers: []string{"@alice:test"}}
	for name, change := range map[string]func(*Config){
		"plain HTTP":      func(c *Config) { c.Homeserver = "http://matrix.test" },
		"no user":         func(c *Config) { c.UserID = "" },
		"no token":        func(c *Config) { c.Token = "" },
		"no device":       func(c *Config) { c.DeviceID = "" },
		"no state":        func(c *Config) { c.StateDir = "" },
		"display name":    func(c *Config) { c.AllowedUsers = []string{"@alice:test", "Alice"} },
		"missing server":  func(c *Config) { c.AllowedUsers = []string{"@alice"} },
		"wrong sigil":     func(c *Config) { c.AllowedUsers = []string{"#alice:test"} },
		"user is no list": func(c *Config) { c.AllowedUsers = []string{""} },
	} {
		cfg := valid
		change(&cfg)
		if c, err := New(cfg); err == nil || c != nil {
			t.Errorf("%s: accepted %+v", name, cfg)
		}
	}
	c, err := New(valid)
	if err != nil || !c.allowed["@alice:test"] || len(c.allowed) != 1 || c.cli.DeviceID != "BOT" || c.cli.UserID != "@bot:test" {
		t.Fatalf("New: %v", err)
	}
}
func TestSafeState(t *testing.T) {
	c := testClient(t, "https://matrix.test")
	for _, tc := range []struct {
		name   string
		change func([]*event.Event) []*event.Event
		ok     bool
	}{
		{"allowed", func(s []*event.Event) []*event.Event { return s }, true},
		{"unencrypted", func(s []*event.Event) []*event.Event { return s[1:] }, false},
		{"wrong algorithm", func(s []*event.Event) []*event.Event {
			s[0].Content.Parsed = &event.EncryptionEventContent{Algorithm: id.AlgorithmOlmV1}
			return s
		}, false},
		{"third joined", func(s []*event.Event) []*event.Event {
			return append(s, stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipJoin}))
		}, false},
		{"third invited", func(s []*event.Event) []*event.Event {
			return append(s, stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipInvite}))
		}, false},
		{"third knocking", func(s []*event.Event) []*event.Event {
			return append(s, stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipKnock}))
		}, false},
		{"third left", func(s []*event.Event) []*event.Event {
			return append(s, stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipLeave}))
		}, true},
		{"allowed invited", func(s []*event.Event) []*event.Event {
			s[2].Content.Parsed = &event.MemberEventContent{Membership: event.MembershipInvite}
			return s
		}, false},
		{"case sensitive", func(s []*event.Event) []*event.Event { *s[2].StateKey = "@Alice:test"; return s }, false},
		{"bot not joined", func(s []*event.Event) []*event.Event {
			s[1].Content.Parsed = &event.MemberEventContent{Membership: event.MembershipInvite}
			return s
		}, false},
		{"bot alone", func(s []*event.Event) []*event.Event { return s[:2] }, false},
		{"peer left", func(s []*event.Event) []*event.Event {
			s[2].Content.Parsed = &event.MemberEventContent{Membership: event.MembershipLeave}
			return s
		}, false},
		{"third banned", func(s []*event.Event) []*event.Event {
			return append(s, stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipBan}))
		}, true},
		// Only state counts: a timeline event or another kind of state
		// cannot add a member.
		{"member without state key", func(s []*event.Event) []*event.Event {
			e := stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipJoin})
			e.StateKey = nil
			return append([]*event.Event{e}, s...)
		}, true},
		{"other state", func(s []*event.Event) []*event.Event {
			return append([]*event.Event{stateEvent(event.StateRoomName, "@eve:test", &event.MemberEventContent{Membership: event.MembershipJoin})}, s...)
		}, true},
		{"unreadable member", func(s []*event.Event) []*event.Event {
			e := stateEvent(event.StateMember, "@eve:test", nil)
			e.Content = event.Content{VeryRaw: json.RawMessage(`{"membership": 5}`)}
			return append(s, e)
		}, false},
		{"unparsed member", func(s []*event.Event) []*event.Event {
			e := stateEvent(event.StateMember, "@alice:test", nil)
			e.Content = event.Content{VeryRaw: json.RawMessage(`{"membership": "join"}`)}
			return append(s[:2], e)
		}, true},
		{"two allowed joined", func(s []*event.Event) []*event.Event {
			c.allowed["@bob:test"] = true
			return append(s, stateEvent(event.StateMember, "@bob:test", &event.MemberEventContent{Membership: event.MembershipJoin}))
		}, false},
		{"empty allowlist", func(s []*event.Event) []*event.Event { c.allowed = map[id.UserID]bool{}; return s }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.safeState(tc.change(safeEvents()))
			if (err == nil) != tc.ok {
				t.Fatalf("safeState error=%v want safe=%v", err, tc.ok)
			}
		})
	}
}
func TestInviteNeedsAnEncryptedRoomOfTwo(t *testing.T) {
	member := func(user, membership string) *event.Event {
		e := stateEvent(event.StateMember, user, &event.MemberEventContent{Membership: event.Membership(membership)})
		e.Sender = "@alice:test"
		return e
	}
	encryption := func(algorithm id.Algorithm) *event.Event {
		return stateEvent(event.StateEncryption, "", &event.EncryptionEventContent{Algorithm: algorithm})
	}
	// As a sync carries them, the events are not parsed yet.
	raw := func(t event.Type, key, content string) *event.Event {
		return &event.Event{Type: t, StateKey: &key, Sender: "@alice:test", Content: event.Content{VeryRaw: json.RawMessage(content)}}
	}
	mallory := func(e *event.Event) *event.Event { e.Sender = "@mallory:test"; return e }
	keyless := func(e *event.Event) *event.Event { e.StateKey = nil; return e }
	invited := member("@bot:test", "invite")
	for _, tc := range []struct {
		name  string
		state []*event.Event
		join  bool
	}{
		{"encrypted", []*event.Event{invited, encryption(id.AlgorithmMegolmV1)}, true},
		{"inviter joined", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), member("@alice:test", "join")}, true},
		{"third left", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), member("@eve:test", "leave")}, true},
		{"unparsed state", []*event.Event{
			raw(event.StateMember, "@bot:test", `{"membership": "invite"}`),
			raw(event.StateEncryption, "", `{"algorithm": "m.megolm.v1.aes-sha2"}`),
			raw(event.StateMember, "@alice:test", `{"membership": "join"}`),
		}, true},
		{"unparsed third", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), raw(event.StateMember, "@eve:test", `{"membership": "join"}`)}, false},
		// Only the invite of the bot names who invited it.
		{"invite of the inviter", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), mallory(member("@alice:test", "invite"))}, true},
		{"invite without state key", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), keyless(mallory(member("@bot:test", "invite")))}, true},
		// A member event without a state key is about nobody.
		{"third without state key", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), keyless(member("@eve:test", "join"))}, true},
		{"not encrypted", []*event.Event{invited}, false},
		{"other algorithm", []*event.Event{invited, encryption(id.AlgorithmOlmV1)}, false},
		{"bot not invited", []*event.Event{member("@bot:test", "join"), encryption(id.AlgorithmMegolmV1)}, false},
		{"third joined", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), member("@eve:test", "join")}, false},
		{"third invited", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), member("@eve:test", "invite")}, false},
		{"third knocking", []*event.Event{invited, encryption(id.AlgorithmMegolmV1), member("@eve:test", "knock")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHomeserver(t)
			c := testClient(t, h.server.URL)
			c.handleInvite(context.Background(), "!invited:test", tc.state)
			if _, joins, _ := h.requests(); (len(joins) == 1) != tc.join || len(joins) > 1 {
				t.Fatalf("joins %v, want a join: %v", joins, tc.join)
			}
		})
	}
}
func TestInviteAllowlist(t *testing.T) {
	joins := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.Contains(r.URL.Path, "/join") {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		joins++
		json.NewEncoder(w).Encode(map[string]string{"room_id": "!dm:test"})
	}))
	defer server.Close()
	c := testClient(t, server.URL)
	for _, sender := range []id.UserID{"@mallory:test", "@Alice:test", "@alice:test"} {
		invite := stateEvent(event.StateMember, "@bot:test", &event.MemberEventContent{Membership: event.MembershipInvite})
		invite.Sender = sender
		c.handleInvite(context.Background(), "!dm:test", []*event.Event{invite, safeEvents()[0]})
	}
	if joins != 1 {
		t.Fatalf("got %d joins, want only exact allowlisted inviter", joins)
	}
	invite := stateEvent(event.StateMember, "@bot:test", &event.MemberEventContent{Membership: event.MembershipInvite})
	invite.Sender = "@alice:test"
	c.handleInvite(context.Background(), "!dm:test", []*event.Event{invite, safeEvents()[0], stateEvent(event.StateMember, "@eve:test", &event.MemberEventContent{Membership: event.MembershipInvite})})
	if joins != 1 {
		t.Fatal("joined visibly unsafe invite")
	}
}
func TestSafeStateFindsThePeer(t *testing.T) {
	c := testClient(t, "https://matrix.test")
	c.allowed["@bob:test"] = true
	state := safeEvents()
	*state[2].StateKey = "@bob:test"
	state = append(state, stateEvent(event.StateMember, "@alice:test", &event.MemberEventContent{Membership: event.MembershipLeave}))
	if peer, err := c.safeState(state); err != nil || peer != "@bob:test" {
		t.Fatalf("peer %q, %v", peer, err)
	}
}
func TestSendChecksTheRoomAroundEncryption(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	c.allowed["@bob:test"] = true
	if err := c.Send(context.Background(), "!dm:test", "too early"); err == nil {
		t.Fatal("sent before Run")
	}
	// Run starts before the encryption does, and ends before Close.
	crypt := &fakeCrypto{}
	c.running = true
	if err := c.Send(context.Background(), "!dm:test", "too early"); err == nil {
		t.Fatal("sent before the encryption started")
	}
	c.crypto, c.running = crypt, false
	if err := c.Send(context.Background(), "!dm:test", "too late"); err == nil {
		t.Fatal("sent after Run")
	}
	c.running = true
	defer func() { c.running = false }()
	h.room("!crowded:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipKnock})
	h.breakRoom("!broken:test", true)
	for _, room := range []string{"!crowded:test", "!broken:test"} {
		if err := c.Send(context.Background(), room, "must not leave"); err == nil {
			t.Fatalf("sent to %s", room)
		}
	}
	// A reply to Bob does not go to Alice, who is in the room now.
	if err := c.SendTo(context.Background(), "!dm:test", "@bob:test", "for Bob"); err == nil {
		t.Fatal("sent the reply of Bob to Alice")
	}
	if crypt.encrypted != 0 {
		t.Fatal("shared keys before the room was checked")
	}
	broken := errors.New("encryption failed")
	crypt.sealing = func() error { return broken }
	if err := c.Send(context.Background(), "!dm:test", "must not leave"); err == nil || errors.Is(err, broken) {
		t.Fatalf("failed encryption: %v", err)
	}
	// The room can change while keys are shared: it is checked again before
	// the message is sent.
	crypt.sealing = func() error { h.breakRoom("!dm:test", true); return nil }
	if err := c.Send(context.Background(), "!dm:test", "must not leave"); err == nil {
		t.Fatal("sent without a second look at the room")
	}
	h.breakRoom("!dm:test", false)
	crypt.sealing = func() error {
		h.room("!bob:test", map[string]event.Membership{"@bob:test": event.MembershipJoin})
		return nil
	}
	if err := c.SendTo(context.Background(), "!bob:test", "@alice:test", "for Alice"); err == nil {
		t.Fatal("sent after Bob took the place of Alice")
	}
	if sent, _, _ := h.requests(); len(sent) != 0 {
		t.Fatalf("sent to %v", sent)
	}
	crypt.sealing = nil
	if err := c.SendTo(context.Background(), "!dm:test", "@alice:test", "for Alice"); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), "!other:test", "for whoever is there"); err != nil {
		t.Fatal(err)
	}
	if sent, _, _ := h.requests(); !slices.Equal(sent, []string{"!dm:test", "!other:test"}) || crypt.encrypted != 5 {
		t.Fatalf("sent to %v after %d encryptions", sent, crypt.encrypted)
	}
}
func TestServerValidation(t *testing.T) {
	for _, server := range []string{
		"http://matrix.example", "https://user:pass@example.org", "https://example.org?token=secret",
		"http://192.0.2.1:8008", "http://localhost.example.org:8008", "ftp://localhost:8008",
		"http://localhost:8008?token=secret", "http://localhost:8008#fragment",
		"http://user:pass@localhost:8008", "https://example.org#fragment", "https://",
	} {
		if ValidateServer(server) == nil {
			t.Errorf("accepted unsafe server %s", server)
		}
	}
	for _, server := range []string{
		"https://matrix.example",
		"http://localhost:8008", "http://127.0.0.1:8008", "http://[::1]:8008",
	} {
		if err := ValidateServer(server); err != nil {
			t.Errorf("rejected allowed server %s: %v", server, err)
		}
		// Construction exercises the transport entry point without making requests.
		c := testClient(t, server)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
