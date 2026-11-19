package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/home"
	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
)

func TestSettingsValidation(t *testing.T) {
	for _, c := range []plugin.GatewayChange{
		{Homeserver: "http://example.org"},
		{Homeserver: "http://192.0.2.1:8008"},
		{Homeserver: "http://100.91.15.23:8008"},
		{Homeserver: "http://100.125.108.63:8008", Values: map[string]string{"allow_http": "false"}},
		{Homeserver: "http://100.125.108.63:8008", Values: map[string]string{"allow_http": "yes"}},
		{Homeserver: "http://100.125.108.63:8008?token=secret", Values: map[string]string{"allow_http": "true"}},
		{Homeserver: "http://user:pass@100.125.108.63:8008", Values: map[string]string{"allow_http": "true"}},
		{Homeserver: "ftp://100.125.108.63:8008", Values: map[string]string{"allow_http": "true"}},
		{Homeserver: "http://localhost.example.org:8008"},
		{Homeserver: "http://localhost:8008?token=secret"},
		{Homeserver: "http://localhost:8008#fragment"},
		{Homeserver: "http://user:pass@localhost:8008"},
		{Homeserver: "ftp://localhost:8008"},
		{Homeserver: "https://user:password@example.org"},
		{Homeserver: "https://example.org?token=secret"},
		{UserID: "luna:example.org"},
		{AllowedUsers: []string{"lucas:example.org"}},
		{Enabled: true},
		{UserID: "@luna:example.org", AllowedUsers: []string{"@luna:example.org"}},
	} {
		if _, err := normalize(c); err == nil {
			t.Errorf("accepted invalid settings: %+v", c)
		}
	}
	c, err := normalize(plugin.GatewayChange{Homeserver: " https://example.org/ ", UserID: " @luna:example.org ", AllowedUsers: []string{"", "@lucas:example.org", " @lucas:example.org ", ""}})
	if err != nil || c.Homeserver != "https://example.org" || c.UserID != "@luna:example.org" || !slices.Equal(c.AllowedUsers, []string{"@lucas:example.org"}) {
		t.Fatalf("normalize: %+v %v", c, err)
	}
	for _, server := range []string{"http://localhost:8008", "http://127.0.0.1:8008", "http://[::1]:8008"} {
		c, err := normalize(plugin.GatewayChange{Homeserver: server})
		if err != nil || c.Homeserver != server {
			t.Errorf("allowed homeserver %s: %+v %v", server, c, err)
		}
	}
	// Plain HTTP elsewhere takes the explicit allowance, for that homeserver.
	c, err = normalize(plugin.GatewayChange{Values: map[string]string{"homeserver": " http://100.125.108.63:8008/ ", "allow_http": "true"}})
	if err != nil || c.Homeserver != "http://100.125.108.63:8008" || httpAllowance(c) != c.Homeserver {
		t.Errorf("allowed tailnet homeserver: %+v %v", c, err)
	}
	// The refusal says nothing of the address; the log gets it.
	_, err = normalize(plugin.GatewayChange{Homeserver: "http://100.125.108.63:8008"})
	var refusal plugin.GatewayRefusal
	if !errors.As(err, &refusal) || !errors.Is(err, transport.ErrServerRefused) || strings.Contains(refusal.Reason, "100.") {
		t.Errorf("refusal: %v", err)
	}
	c, err = normalize(plugin.GatewayChange{Homeserver: " http://localhost:8008/ "})
	if err != nil || c.Homeserver != "http://localhost:8008" {
		t.Fatalf("normalize loopback endpoint: %+v %v", c, err)
	}
	// Empty is deliberately not a wildcard.
	c, err = normalize(plugin.GatewayChange{})
	if err != nil || len(c.AllowedUsers) != 0 {
		t.Fatalf("empty allowlist: %+v %v", c, err)
	}
}

func TestProfileStoreAndSecretSnapshot(t *testing.T) {
	a, b := store{t.TempDir()}, store{t.TempDir()}
	if err := a.create(); err != nil {
		t.Fatal(err)
	}
	c := configuration{Enabled: true, Homeserver: "https://example.org", UserID: "@bot:example.org", Token: "super-secret-token", DeviceID: "device", Revision: "1"}
	if err := a.save(c); err != nil {
		t.Fatal(err)
	}
	got, err := a.load()
	if err != nil || got.Token != c.Token {
		t.Fatalf("load: %+v %v", got, err)
	}
	public, err := a.snapshot()
	if err != nil || !public.LoggedIn || public.Status != "Waiting for the node" {
		t.Fatalf("snapshot %+v %v", public, err)
	}
	data, _ := os.ReadFile(a.path("settings.json"))
	if strings.Contains(string(data), "password") {
		t.Fatal("password persisted")
	}
	fi, err := os.Stat(a.path("settings.json"))
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("secret mode: %v %v", fi, err)
	}
	other, err := b.snapshot()
	if err != nil || other.LoggedIn || other.UserID != "" {
		t.Fatalf("profile leak: %+v %v", other, err)
	}
	a.status(c.Revision, "Connected")
	public, err = a.snapshot()
	if err != nil || public.Status != "Connected" {
		t.Fatalf("status: %+v %v", public, err)
	}
	c.Revision = "2"
	if err := a.save(c); err != nil {
		t.Fatal(err)
	}
	public, _ = a.snapshot()
	if public.Status == "Connected" {
		t.Fatal("stale status after configuration changed")
	}
}

func TestNormalizeTakesTheFieldsOfTheForm(t *testing.T) {
	c, err := normalize(plugin.GatewayChange{
		Values: map[string]string{"homeserver": " https://example.org/ ", "user_id": "@luna:example.org", "password": "secret"},
		Lists:  map[string][]string{"allowed_users": {"@lucas:example.org", "@ada:example.org"}},
	})
	if err != nil || c.Homeserver != "https://example.org" || c.UserID != "@luna:example.org" || c.Password != "secret" ||
		!slices.Equal(c.AllowedUsers, []string{"@ada:example.org", "@lucas:example.org"}) {
		t.Fatalf("normalize: %+v %v", c, err)
	}
	for _, change := range []plugin.GatewayChange{
		{Values: map[string]string{"token": "fixture-token"}},
		{Lists: map[string][]string{"admins": {"@lucas:example.org"}}},
		// A password needs the account it is for.
		{Password: "secret", Homeserver: "https://example.org"},
		{Password: "secret", UserID: "@luna:example.org"},
		{Enabled: true, Homeserver: "https://example.org"},
		{Enabled: true, UserID: "@luna:example.org"},
		// User IDs are complete.
		{UserID: "@luna"},
		{UserID: "@:example.org"},
		{UserID: "@luna:"},
		{UserID: "@luna :example.org"},
		{AllowedUsers: []string{"@lucas\t:example.org"}},
	} {
		if _, err := normalize(change); err == nil {
			t.Errorf("accepted %+v", change)
		}
	}
	if c, err := normalize(plugin.GatewayChange{Password: "secret", Homeserver: "https://example.org", UserID: "@l:e"}); err != nil || c.UserID != "@l:e" {
		t.Fatalf("shortest user ID: %+v %v", c, err)
	}
}

func TestStoreReportsUnreadableState(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (store{filepath.Join(blocked, "matrix")}).create(); err == nil {
		t.Fatal("created the store below a file")
	}
	s := store{t.TempDir()}
	if err := os.Mkdir(s.path("settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load(); err == nil {
		t.Fatal("read settings from a directory")
	}
	if _, err := s.snapshot(); err == nil {
		t.Fatal("showed settings that cannot be read")
	}
	os.Remove(s.path("settings.json"))
	if err := os.WriteFile(s.path("settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.snapshot(); err == nil {
		t.Fatal("showed damaged settings")
	}
}

func TestSnapshotStatus(t *testing.T) {
	s := store{t.TempDir()}
	c := configuration{Homeserver: "https://example.org", UserID: "@bot:example.org", AllowedUsers: []string{"@owner:example.org"}, Revision: "1"}
	status := func() string {
		t.Helper()
		public, err := s.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		return public.Status
	}
	if err := s.save(c); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "Disabled" {
		t.Fatalf("disabled: %q", got)
	}
	c.Enabled = true
	if err := s.save(c); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "Not logged in" {
		t.Fatalf("without login: %q", got)
	}
	c.Token = "fixture-token"
	if err := s.save(c); err != nil {
		t.Fatal(err)
	}
	s.status("1", "Connected")
	public, err := s.snapshot()
	if err != nil || public.Status != "Connected" || !public.Enabled || !public.LoggedIn || public.Homeserver != c.Homeserver || public.UserID != c.UserID {
		t.Fatalf("snapshot %+v %v", public, err)
	}
	// The form shows the public settings, never the token or a password.
	values := map[string]string{}
	for _, field := range public.Fields {
		values[field.Key] = field.Value + strings.Join(field.List, ",")
	}
	if values["homeserver"] != c.Homeserver || values["user_id"] != c.UserID || values["password"] != "" || values["allowed_users"] != "@owner:example.org" || values["allow_http"] != "" || len(values) != 5 {
		t.Fatalf("fields %v", values)
	}
	public.Fields[4].List[0] = "@eve:example.org"
	if again, _ := s.snapshot(); again.Fields[4].List[0] != "@owner:example.org" || again.AllowedUsers[0] != "@owner:example.org" {
		t.Fatal("the form shares its list with the settings")
	}
	// A status the node wrote long ago, or that cannot be read, says nothing.
	data, _ := json.Marshal(connectionStatus{Revision: "1", Text: "Connected", At: time.Now().Add(-20 * time.Second)})
	if err := os.WriteFile(s.path("status.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "Waiting for the node" {
		t.Fatalf("old status: %q", got)
	}
	data, _ = json.Marshal(connectionStatus{Revision: "1", Text: "Connected", At: time.Now().Add(-10 * time.Second)})
	if err := os.WriteFile(s.path("status.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "Connected" {
		t.Fatalf("recent status: %q", got)
	}
	damaged := fmt.Sprintf(`{"Revision": "1", "Text": 5, "At": %q}`, time.Now().Format(time.RFC3339Nano))
	if err := os.WriteFile(s.path("status.json"), []byte(damaged), 0600); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "Waiting for the node" {
		t.Fatalf("damaged status: %q", got)
	}
}

func TestCreateKeepsTheStorePrivate(t *testing.T) {
	s := store{filepath.Join(t.TempDir(), "matrix")}
	if err := os.Mkdir(s.dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(s.dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("mode %v %v", info.Mode().Perm(), err)
	}
}

func TestHTTPAllowanceIsSavedForExactlyThatHomeserver(t *testing.T) {
	const tailnet = "http://100.125.108.63:8008"
	s := store{t.TempDir()}
	var allowedAtLogin string
	login := func(_ context.Context, _, httpAllowed, user, _ string) (loginResult, error) {
		allowedAtLogin = httpAllowed
		return loginResult{UserID: user, Token: "fixture-token", DeviceID: "device"}, nil
	}
	change := plugin.GatewayChange{Enabled: true, UserID: "@bot:example.org", Password: "fixture-password",
		Values: map[string]string{"homeserver": tailnet, "allow_http": "true"}}
	if err := s.change(context.Background(), change, login); err != nil {
		t.Fatal(err)
	}
	if allowedAtLogin != tailnet {
		t.Fatalf("login got the allowance %q", allowedAtLogin)
	}
	data, _ := os.ReadFile(s.path("settings.json"))
	if !strings.Contains(string(data), `"http_allowed": "`+tailnet+`"`) || strings.Contains(string(data), "fixture-password") {
		t.Fatalf("saved %s", data)
	}
	public, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var box, homeserver plugin.GatewayField
	for _, f := range public.Fields {
		switch f.Key {
		case "allow_http":
			box = f
		case "homeserver":
			homeserver = f
		}
	}
	if box.Kind != "bool" || !box.On || box.Hint == "" || homeserver.Hint == "" {
		t.Fatalf("form %+v %+v", box, homeserver)
	}
	// Without the box the same homeserver is refused and nothing is replaced.
	change.Password, change.Enabled = "", false
	change.Values = map[string]string{"homeserver": tailnet, "allow_http": "false"}
	if err := s.change(context.Background(), change, nil); !errors.Is(err, transport.ErrServerRefused) {
		t.Fatalf("saved plain HTTP without the allowance: %v", err)
	}
	if c, _ := s.load(); c.HTTPAllowed != tailnet {
		t.Fatalf("a refused change replaced the settings: %+v", c)
	}
	// Another homeserver does not inherit it: the box must be ticked again.
	change.Values = map[string]string{"homeserver": "http://100.125.108.63:8009"}
	if err := s.change(context.Background(), change, nil); !errors.Is(err, transport.ErrServerRefused) {
		t.Fatalf("saved another homeserver on the old allowance: %v", err)
	}
	// HTTPS and loopback keep no allowance, and drop the login of another homeserver.
	for _, server := range []string{"https://example.org", "http://127.0.0.1:8008"} {
		change.Values = map[string]string{"homeserver": server, "allow_http": "true"}
		if err := s.change(context.Background(), change, nil); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.load(); c.HTTPAllowed != "" && !strings.HasPrefix(server, "http://") || c.HTTPAllowed != server && strings.HasPrefix(server, "http://") {
			t.Fatalf("allowance for %s: %+v", server, c)
		}
	}
}

func TestRefusedHomeserverIsLoggedWithoutCredentials(t *testing.T) {
	dir := home.Dir(t.TempDir())
	gateway, serving := startPlugin(t, dir, &testSessions{})
	err := gateway.Save(context.Background(), plugin.GatewayChange{Homeserver: "http://user:pw@100.125.108.63:8008/base?token=secret#frag", UserID: "@bot:example.org", Password: "fixture-password"})
	if err == nil {
		t.Fatal("saved a homeserver with credentials")
	}
	err = gateway.Save(context.Background(), plugin.GatewayChange{Homeserver: "http://100.125.108.63:8008/base", UserID: "@bot:example.org", Password: "fixture-password"})
	if err == nil {
		t.Fatal("saved plain HTTP without the allowance")
	}
	// A refusal of anything else is not logged as one of the homeserver.
	_ = gateway.Save(context.Background(), plugin.GatewayChange{UserID: "luna:example.org"})
	serving.Start()
	serving.Stop()
	log, err := os.ReadFile(dir.Service().LogFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(log)
	if !strings.Contains(text, `homeserver "http://100.125.108.63:8008/base" refused`) {
		t.Fatalf("log %q", text)
	}
	if strings.Count(text, " refused: ") != 2 || strings.ContainsAny(text, "?#") && strings.Contains(text, "token") ||
		strings.Contains(text, "pw") || strings.Contains(text, "secret") || strings.Contains(text, "fixture-password") {
		t.Fatalf("log %q", text)
	}
}

func TestSavedHomeserverRefusedAtStartIsLoggedRedacted(t *testing.T) {
	dir := home.Dir(t.TempDir())
	s := store{filepath.Join(dir.Gateways(), "matrix")}
	// Settings of an older version: plain HTTP to a tailnet address, no allowance.
	old := configuration{Enabled: true, Homeserver: "http://100.91.15.23:8008", UserID: "@bot:example.org", Token: "fixture-token", DeviceID: "BOT", Revision: "1"}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(old); err != nil {
		t.Fatal(err)
	}
	_, serving := startPlugin(t, dir, &testSessions{})
	serving.Start()
	defer serving.Stop()
	var text string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !strings.Contains(text, "refused"); time.Sleep(20 * time.Millisecond) {
		log, _ := os.ReadFile(dir.Service().LogFile)
		text = string(log)
	}
	if !strings.Contains(text, `saved homeserver "http://100.91.15.23:8008" refused`) || strings.Contains(text, "fixture-token") {
		t.Fatalf("log %q", text)
	}
}
