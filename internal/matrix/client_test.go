package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
