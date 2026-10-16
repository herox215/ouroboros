package matrix

import (
	"os"
	"strings"
	"testing"

	"ouroboros/plugin"
)

func TestSettingsValidation(t *testing.T) {
	for _, c := range []plugin.GatewayChange{
		{Homeserver: "http://example.org"},
		{Homeserver: "http://192.0.2.1:8008"},
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
	c, err := normalize(plugin.GatewayChange{Homeserver: " https://example.org/ ", UserID: " @luna:example.org ", AllowedUsers: []string{"@lucas:example.org", " @lucas:example.org ", ""}})
	if err != nil || c.Homeserver != "https://example.org" || len(c.AllowedUsers) != 1 {
		t.Fatalf("normalize: %+v %v", c, err)
	}
	for _, server := range []string{
		"http://localhost:8008",
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
