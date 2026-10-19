package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

// sessionProvider offers sessions to the gateway as the node does.
func sessionProvider(s plugin.Sessions) plugin.Plugin {
	return plugin.NewService("sessions", func(*plugin.Context, struct{}) (struct{ Sessions plugin.Sessions }, error) {
		return struct{ Sessions plugin.Sessions }{s}, nil
	})
}

// startPlugin starts the gateway of dir with sessions and returns what it
// contributes.
func startPlugin(t *testing.T, dir home.Dir, sessions plugin.Sessions) (plugin.Gateway, plugin.Serving) {
	t.Helper()
	k := kernel.New()
	if err := k.Start(sessionProvider(sessions), Plugin(dir, nil)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	gateways, servings := plugin.All[plugin.Gateway](k.Context()), plugin.All[plugin.Serving](k.Context())
	if len(gateways) != 1 || len(servings) != 1 {
		t.Fatalf("%d gateways, %d servings", len(gateways), len(servings))
	}
	return gateways[0], servings[0]
}

func TestPluginLogsInOverTheHomeserver(t *testing.T) {
	dir := home.Dir(t.TempDir())
	gateway, _ := startPlugin(t, dir, &testSessions{})
	if gateway.Name != "matrix" || gateway.Title != "Matrix" {
		t.Fatalf("gateway %q %q", gateway.Name, gateway.Title)
	}
	settings, err := gateway.Settings(context.Background())
	if err != nil || settings.Status != "Disabled" || settings.LoggedIn {
		t.Fatalf("before the login: %+v %v", settings, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Identifier struct{ User string }
			Password   string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Password != "fixture-password" {
			// A homeserver can echo what it was sent.
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"errcode": "M_FORBIDDEN", "error": "wrong password " + req.Password})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"user_id": req.Identifier.User, "access_token": "fixture-token", "device_id": "BOT"})
	}))
	defer server.Close()
	change := plugin.GatewayChange{Enabled: true, Homeserver: server.URL, UserID: "@bot:example.org", Password: "fixture-guess", AllowedUsers: []string{"@owner:example.org"}}
	if err := gateway.Save(context.Background(), change); err == nil || strings.Contains(err.Error(), "fixture-guess") {
		t.Fatalf("wrong password: %v", err)
	}
	change.Password = "fixture-password"
	if err := gateway.Save(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	settings, err = gateway.Settings(context.Background())
	if err != nil || !settings.LoggedIn || !settings.Enabled || settings.Homeserver != server.URL || settings.UserID != "@bot:example.org" ||
		!slices.Equal(settings.AllowedUsers, []string{"@owner:example.org"}) || settings.Status != "Waiting for the node" {
		t.Fatalf("after the login: %+v %v", settings, err)
	}
	data, err := os.ReadFile(filepath.Join(dir.Gateways(), "matrix", "settings.json"))
	if err != nil || !strings.Contains(string(data), "fixture-token") || strings.Contains(string(data), "fixture-password") {
		t.Fatalf("saved %s %v", data, err)
	}
}

func TestPluginServesOnlyWithItsState(t *testing.T) {
	dir := home.Dir(t.TempDir())
	if err := os.WriteFile(dir.Gateways(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, serving := startPlugin(t, dir, &testSessions{})
	serving.Start()
	serving.Stop()
	if log, err := os.ReadFile(dir.Service().LogFile); err != nil || !strings.Contains(string(log), "matrix gateway state cannot be opened") {
		t.Fatalf("log %q %v", log, err)
	}
}

func TestPluginConnectsWithTheSavedLogin(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/account/whoami") {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"user_id": "@someone:example.org", "device_id": "OTHER"})
	}))
	defer server.Close()
	dir := home.Dir(t.TempDir())
	s := store{filepath.Join(dir.Gateways(), "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	cfg := configuration{Enabled: true, Homeserver: server.URL, UserID: "@bot:example.org", Token: "fixture-token", DeviceID: "BOT", AllowedUsers: []string{"@owner:example.org"}, Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	// Conversations that cannot be restored keep the gateway off the homeserver.
	if err := os.WriteFile(s.path("conversations.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	_, serving := startPlugin(t, dir, &testSessions{})
	serving.Start()
	defer serving.Stop()
	waitStatus(t, s, "1", "Connection failed; retrying")
	os.Remove(s.path("conversations.json"))
	// So does a login without its device.
	cfg.DeviceID, cfg.Revision = "", "2"
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "2", "Connection failed; retrying")
	if requests.Load() != 0 {
		t.Fatal("connected with what it cannot use")
	}
	// A homeserver that knows the token as somebody else's is no connection.
	cfg.DeviceID, cfg.Revision = "BOT", "3"
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "3", "Connection failed; retrying")
	if requests.Load() == 0 {
		t.Fatal("did not ask the homeserver")
	}
	serving.Stop()
	waitStatus(t, s, "3", "Stopped")
}

func TestReceiveOnlyOverTheActiveConnection(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
	for _, conn := range []connection{&recordingConnection{}, guardedConnection{connection: &recordingConnection{}, store: s, revision: "1"}} {
		if err := receive(context.Background(), conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
			t.Fatalf("over %T: %v", conn, err)
		}
	}
	active := &sessionConnection{connection: &recordingConnection{}, conversations: c, store: s, revision: "1"}
	if err := receive(context.Background(), guardedConnection{connection: active, store: s, revision: "1"}, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.asks != 1 {
		t.Fatalf("asked %d times", sessions.asks)
	}
}

func TestSessionConnectionPublishesWhileItRuns(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.receive(context.Background(), &recordingConnection{}, incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "the answer"})
	inner := newLiveConnection()
	inner.ending = make(chan error, 1)
	ended := errors.New("sync ended")
	// The connection ends once the answer went out over it.
	inner.sending = func() {
		select {
		case inner.ending <- ended:
		default:
		}
	}
	active := &sessionConnection{connection: inner, conversations: c, store: s, revision: "1"}
	if got := active.Status(); got != "Synchronizing" {
		t.Fatalf("status %q", got)
	}
	if err := active.Run(context.Background(), func(context.Context, incoming) error { return nil }); !errors.Is(err, ended) {
		t.Fatalf("Run: %v", err)
	}
	if sent := inner.messages(); !slices.Equal(sent, []string{"!room:example.org @owner:example.org the answer"}) {
		t.Fatalf("sent %q", sent)
	}
	if got := (&sessionConnection{connection: healthyConnection{inner}}).Status(); got != "Synced" {
		t.Fatalf("status %q", got)
	}
}
