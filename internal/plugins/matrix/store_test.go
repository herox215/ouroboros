package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/plugin"
)

func TestSettingsValidation(t *testing.T) {
	for _, c := range []plugin.GatewayChange{
		{Homeserver: "http://example.org"},
		{Homeserver: "http://192.0.2.1:8008"},
		{Homeserver: "http://100.91.15.24:8008"},
		{Homeserver: "http://100.91.15.23:8009"},
		{Homeserver: "http://100.91.15.23:8008/path"},
		{Homeserver: "http://user:pass@100.91.15.23:8008"},
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
	for _, server := range []string{
		"http://100.91.15.23:8008", "http://localhost:8008",
		"http://127.0.0.1:8008", "http://[::1]:8008",
	} {
		c, err := normalize(plugin.GatewayChange{Homeserver: server})
		if err != nil || c.Homeserver != server {
			t.Errorf("allowed homeserver %s: %+v %v", server, c, err)
		}
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
	if values["homeserver"] != c.Homeserver || values["user_id"] != c.UserID || values["password"] != "" || values["allowed_users"] != "@owner:example.org" || len(values) != 4 {
		t.Fatalf("fields %v", values)
	}
	public.Fields[3].List[0] = "@eve:example.org"
	if again, _ := s.snapshot(); again.Fields[3].List[0] != "@owner:example.org" || again.AllowedUsers[0] != "@owner:example.org" {
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
