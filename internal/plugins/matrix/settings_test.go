package matrix

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/plugin"
)

func TestLoginPersistsTokenNotPasswordAndKeepsOnEdit(t *testing.T) {
	s := store{t.TempDir()}
	change := plugin.GatewayChange{Enabled: true, Homeserver: "https://example.org", UserID: "@bot:example.org", Password: "do-not-save", AllowedUsers: []string{"@owner:example.org"}}
	login := func(ctx context.Context, server, user, password string) (loginResult, error) {
		if password != "do-not-save" || server != change.Homeserver || user != change.UserID {
			t.Fatal("wrong login input")
		}
		return loginResult{UserID: user, Token: "token", DeviceID: "device"}, nil
	}
	if err := s.change(context.Background(), change, login); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.path("settings.json"))
	if strings.Contains(string(data), "do-not-save") {
		t.Fatal("password saved")
	}
	change.Password = ""
	change.AllowedUsers = nil
	if err := s.change(context.Background(), change, nil); err != nil {
		t.Fatal(err)
	}
	c, _ := s.load()
	if c.Token != "token" || len(c.AllowedUsers) != 0 {
		t.Fatalf("edit: %+v", c)
	}
	change.Logout = true
	if err := s.change(context.Background(), change, nil); err != nil {
		t.Fatal(err)
	}
	c, _ = s.load()
	if c.Enabled || c.Token != "" {
		t.Fatal("logout retained credentials")
	}
}

func TestTheOwnerIsToldWhyAChangeWasRefused(t *testing.T) {
	s := store{t.TempDir()}
	for _, c := range []plugin.GatewayChange{
		{Homeserver: "ftp://example.org", Enabled: true, UserID: "@bot:example.org"},
		{Enabled: true, Homeserver: "https://example.org", UserID: "bot:example.org"},
		{Enabled: true, Homeserver: "https://example.org", UserID: "@bot:example.org"},
	} {
		err := s.change(context.Background(), c, nil)
		var refusal plugin.GatewayRefusal
		if !errors.As(err, &refusal) || strings.TrimSpace(refusal.Reason) == "" {
			t.Fatalf("not a refusal the owner can read: %v", err)
		}
	}
}

func TestFailedLoginIsSanitizedAndDoesNotReplaceSettings(t *testing.T) {
	s := store{t.TempDir()}
	old := configuration{UserID: "@old:example.org", Token: "old-token"}
	if err := s.save(old); err != nil {
		t.Fatal(err)
	}
	change := plugin.GatewayChange{Enabled: true, Homeserver: "https://example.org", UserID: "@bot:example.org", Password: "secret"}
	for _, login := range []loginFunc{
		func(context.Context, string, string, string) (loginResult, error) {
			return loginResult{}, errors.New("secret echoed by server")
		},
		func(context.Context, string, string, string) (loginResult, error) {
			return loginResult{UserID: "@wrong:example.org", Token: "token", DeviceID: "device"}, nil
		},
	} {
		err := s.change(context.Background(), change, login)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("login error: %v", err)
		}
		c, _ := s.load()
		if c.Token != old.Token || c.UserID != old.UserID {
			t.Fatal("failed login changed account")
		}
	}
}

func TestChangeSavesNothingItCannotUse(t *testing.T) {
	refuse := func(context.Context, string, string, string) (loginResult, error) {
		t.Fatal("logged in for a change that cannot be saved")
		return loginResult{}, nil
	}
	valid := plugin.GatewayChange{Homeserver: "https://example.org", UserID: "@bot:example.org"}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (store{filepath.Join(blocked, "matrix")}).change(context.Background(), valid, refuse); err == nil {
		t.Fatal("saved below a file")
	}
	s := store{t.TempDir()}
	if err := s.change(context.Background(), plugin.GatewayChange{Homeserver: "http://example.org", Password: "secret"}, refuse); err == nil {
		t.Fatal("saved an invalid change")
	}
	if err := os.Mkdir(s.path("settings.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.change(context.Background(), valid, refuse); err == nil {
		t.Fatal("saved without the lock")
	}
	os.Remove(s.path("settings.lock"))
	if err := os.WriteFile(s.path("settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.change(context.Background(), valid, refuse); err == nil {
		t.Fatal("replaced settings it could not read")
	}
	os.Remove(s.path("settings.json"))
	valid.Enabled = true
	if err := s.change(context.Background(), valid, refuse); err == nil || !strings.Contains(err.Error(), "log in") {
		t.Fatalf("enabled without a login: %v", err)
	}
	if _, err := os.Stat(s.path("settings.json")); !os.IsNotExist(err) {
		t.Fatalf("saved settings: %v", err)
	}
}

func TestChangeKeepsTheLoginOfTheSameAccountOnly(t *testing.T) {
	s := store{t.TempDir()}
	login := func(_ context.Context, _, user, _ string) (loginResult, error) {
		return loginResult{UserID: user, Token: "token of " + user, DeviceID: "device"}, nil
	}
	change := plugin.GatewayChange{Enabled: true, Homeserver: "https://example.org", UserID: "@bot:example.org", Password: "secret"}
	if err := s.change(context.Background(), change, login); err != nil {
		t.Fatal(err)
	}
	first, _ := s.load()
	change.Password = ""
	if err := s.change(context.Background(), change, nil); err != nil {
		t.Fatal(err)
	}
	second, _ := s.load()
	if second.Token != "token of @bot:example.org" || second.DeviceID != "device" || second.Revision == first.Revision || len(second.Revision) != 32 {
		t.Fatalf("edit: %+v after %+v", second, first)
	}
	// Another account, or the same name on another homeserver, needs a login of its own.
	for _, other := range []plugin.GatewayChange{
		{Homeserver: "https://example.org", UserID: "@other:example.org"},
		{Homeserver: "https://matrix.example.org", UserID: "@bot:example.org"},
	} {
		if err := s.change(context.Background(), change, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.change(context.Background(), other, nil); err != nil {
			t.Fatal(err)
		}
		if c, _ := s.load(); c.Token != "" || c.DeviceID != "" {
			t.Fatalf("kept the login for %+v: %+v", other, c)
		}
		if err := s.change(context.Background(), plugin.GatewayChange{Homeserver: "https://example.org", UserID: "@bot:example.org", Password: "secret"}, login); err != nil {
			t.Fatal(err)
		}
	}
	// A device without a token, or a token without a device, is no login.
	for _, incomplete := range []loginResult{{UserID: "@bot:example.org", DeviceID: "device"}, {UserID: "@bot:example.org", Token: "token"}} {
		result := incomplete
		err := s.change(context.Background(), plugin.GatewayChange{Homeserver: "https://example.org", UserID: "@bot:example.org", Password: "secret"},
			func(context.Context, string, string, string) (loginResult, error) { return result, nil })
		if err == nil {
			t.Fatalf("accepted the login %+v", result)
		}
	}
}
