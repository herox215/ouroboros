package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"ouroboros/plugin"
)

type gatewayBackend struct {
	*fakeBackend
	saved   plugin.GatewayChange
	name    string
	fail    bool
	capture func(plugin.GatewayChange)
}

func (b *gatewayBackend) Gateways(context.Context) ([]plugin.GatewaySettings, error) {
	return []plugin.GatewaySettings{{Name: "fixture", Title: "Matrix", Status: "Ready", Enabled: b.saved.Enabled}}, nil
}
func (b *gatewayBackend) SaveGateway(_ context.Context, name string, c plugin.GatewayChange) error {
	b.name, b.saved = name, c
	if b.capture != nil {
		b.capture(c)
	}
	if b.fail {
		return errors.New("secret password")
	}
	return nil
}

func TestGatewaySettingsNavigationAndCredentials(t *testing.T) {
	m, f := openSettings(t, someSettings())
	b := &gatewayBackend{fakeBackend: f}
	m.backend = b
	deliver(m, m.config.reload(m))
	c := &m.config
	for i, s := range sections {
		if s == gatewaysSection {
			c.category = i
		}
	}
	c.opened = true
	do(m)
	if c.gateway != "fixture" || !strings.Contains(dialogView(m), "Matrix") {
		t.Fatal("gateway not opened")
	}
	// Edit password, observe masked rendering, cancel and inspect the input buffer.
	rows := c.gatewayPage(m)
	rows[4].actions[0].run(m)
	c.input.SetValue("secret")
	if c.input.EchoMode != textinput.EchoPassword || strings.Contains(dialogView(m), "secret") {
		t.Fatal("password is visible")
	}
	press(m, "esc")
	if c.input.Value() != "" || c.editing != nil {
		t.Fatal("cancel retained password")
	}
	rows[4].actions[0].run(m)
	c.input.SetValue("secret")
	deliver(m, press(m, "enter"))
	if b.name != "fixture" || b.saved.Password != "secret" || c.input.Value() != "" {
		t.Fatal("save failed or retained input")
	}
	// An error from a contribution must not echo credentials into notices.
	b.fail = true
	msg := c.saveGateway(m, "secret", false)()
	c.changed(m, msg.(changedMsg))
	if strings.Contains(m.notice, "secret") {
		t.Fatal("credential-bearing error exposed")
	}
	b.fail = false
	deliver(m, c.saveGateway(m, "", true))
	if !b.saved.Logout {
		t.Fatal("logout not sent")
	}
	c.leave(m)
	if c.gateway != "" || c.gatewayDraft.Password != "" {
		t.Fatal("gateway not closed")
	}
	c.gatewayEdit(m, "Password", "", true, func(string) tea.Cmd { return nil })
	c.input.SetValue("secret")
	press(m, "ctrl+c")
	if c.input.Value() != "" {
		t.Fatal("quit retained password")
	}
}

func TestGatewaySchemaWithoutMatrixFields(t *testing.T) {
	m, f := openSettings(t, someSettings())
	b := &gatewayBackend{fakeBackend: f}
	m.backend = b
	c := &m.config
	c.gateways = []plugin.GatewaySettings{{Name: "custom", Fields: []plugin.GatewayField{
		{Key: "token", Label: "Bot token", Kind: "secret", Value: "must-ignore"},
		{Key: "region", Label: "Region", Kind: "text", Value: "west"},
		{Key: "members", Label: "Members", Kind: "list", List: []string{"one"}},
		{Key: "quiet", Label: "Quiet", Kind: "bool", On: true},
		{Key: "other", Label: "Other secret", Kind: "secret"},
	}}}
	c.gatewayRows(m)[0].actions[0].run(m)
	rows := c.gatewayPage(m)
	for _, r := range rows {
		if r.label == "Homeserver" || r.label == "User ID" || r.label == "Password" {
			t.Fatal("legacy field in schema")
		}
	}
	rows[2].actions[0].run(m)
	c.input.SetValue("token-secret")
	if c.input.EchoMode != textinput.EchoPassword || strings.Contains(dialogView(m), "token-secret") {
		t.Fatal("secret visible")
	}
	press(m, "enter")
	rows[3].actions[0].run(m)
	c.input.SetValue("east")
	press(m, "enter")
	rows[4].actions[0].run(m)
	c.input.SetValue("two, three, four")
	press(m, "enter")
	rows[5].actions[0].run(m)
	rows[6].actions[0].run(m)
	c.input.SetValue("other-secret")
	press(m, "enter")
	if strings.Contains(dialogView(m), "token-secret") || strings.Contains(dialogView(m), "other-secret") {
		t.Fatal("draft visible")
	}
	cmd := c.saveGateway(m, "", false)
	if c.gatewayDraft.Values["token"] != "" || c.gatewayDraft.Values["other"] != "" {
		t.Fatal("save retained secrets")
	}
	// Observe the payload before its transient secret entries are cleared.
	b.capture = func(change plugin.GatewayChange) {
		if change.Values["token"] != "token-secret" || change.Values["other"] != "other-secret" || change.Values["region"] != "east" || change.Values["quiet"] != "false" || strings.Join(change.Lists["members"], ",") != "two,three,four" || change.Homeserver != "" || change.UserID != "" {
			t.Fatalf("wrong payload: %#v", change)
		}
	}
	cmd()
	b.capture = nil
	c.gatewayDraft.Values["token"], c.gatewayDraft.Values["other"] = "discard", "discard"
	rows[2].actions[0].run(m)
	c.input.SetValue("cancel")
	press(m, "esc")
	if c.input.Value() != "" || c.gatewayDraft.Values["token"] != "" || c.gatewayDraft.Values["other"] != "" {
		t.Fatal("cancel retained secrets")
	}
	c.gatewayDraft.Values["token"] = "quit"
	press(m, "ctrl+c")
	if c.gatewayDraft.Values["token"] != "" {
		t.Fatal("quit retained secret")
	}
}
