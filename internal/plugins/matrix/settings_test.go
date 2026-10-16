package matrix

import (
	"context"
	"errors"
	"os"
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
