package tui

import (
	"context"
	"errors"
	"maps"
	"slices"
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
	err     error
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
	if b.err != nil {
		return b.err
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
	// A reason the contribution declares safe to show is shown, on one line.
	b.fail, b.err = false, plugin.GatewayRefusal{Reason: "use a complete Matrix user ID, such as @luna:example.org\n"}
	msg = c.saveGateway(m, "secret", false)()
	c.changed(m, msg.(changedMsg))
	if want := "Gateway not saved: use a complete Matrix user ID, such as @luna:example.org"; m.notice != want {
		t.Fatalf("notice: %q", m.notice)
	}
	b.err = nil
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

// choose selects the action name of the row label of what is open, without
// leaving it.
func choose(t *testing.T, m *Model, label, name string) {
	t.Helper()
	for i, r := range m.config.shown(m) {
		if r.label != label {
			continue
		}
		for j, a := range r.actions {
			if a.name == name {
				m.config.sel, m.choice = i, j
				return
			}
		}
	}
	t.Fatalf("no %s %s in %+v", label, name, m.config.shown(m))
}

// shownValue is the value of the row label of what is open.
func shownValue(t *testing.T, m *Model, label string) string {
	t.Helper()
	for _, r := range m.config.shown(m) {
		if r.label == label {
			return r.value
		}
	}
	t.Fatalf("no %s in %+v", label, m.config.shown(m))
	return ""
}

// typeInto runs the edit of the row label of what is open with text typed.
func typeInto(t *testing.T, m *Model, label, text string) {
	t.Helper()
	choose(t, m, label, "Edit")
	press(m, "enter")
	if m.config.editing == nil {
		t.Fatalf("%s is not edited", label)
	}
	m.config.input.SetValue(text)
	do(m)
}

// A gateway without a form of its own has the fields of Matrix: its page
// says whether it is logged in and on, takes the homeserver, the user and
// the users it allows as they are typed, and saves them or logs out.
func TestGatewayMatrixPage(t *testing.T) {
	m, f := openSettings(t, someSettings())
	b := &gatewayBackend{fakeBackend: f}
	m.backend = b
	c := &m.config
	c.gateways = []plugin.GatewaySettings{{Name: "matrix", Title: "Matrix", Enabled: true, LoggedIn: true, Status: "Synchronizing",
		Homeserver: "https://old.example", UserID: "@old:old.example", AllowedUsers: []string{"@sol:old.example"}}}
	if rows := c.gatewayRows(m); len(rows) != 1 || rows[0].label != "Matrix" {
		t.Fatalf("the gateways: %+v", rows)
	}
	pick(t, m, gatewaysSection, "Matrix", "Open")
	do(m)
	if c.gateway != "matrix" || !strings.Contains(dialogView(m), "Settings › Gateways › Matrix") {
		t.Fatalf("not opened:\n%s", dialogView(m))
	}
	for label, want := range map[string]string{"Status": "Logged in · Synchronizing", "Enabled": "On", "Homeserver": "https://old.example",
		"User ID": "@old:old.example", "Allowed users": "@sol:old.example", "Password": "Not stored in the UI", "Access": gatewayAccess} {
		if got := shownValue(t, m, label); got != want {
			t.Errorf("%s: %q", label, got)
		}
	}
	choose(t, m, "Enabled", "Toggle")
	if do(m); shownValue(t, m, "Enabled") != "Off" {
		t.Errorf("toggled: %q", shownValue(t, m, "Enabled"))
	}
	typeInto(t, m, "Homeserver", "https://matrix.example")
	if m.config.input.Value() != "" {
		t.Errorf("the input keeps what was typed for the gateway: %q", m.config.input.Value())
	}
	typeInto(t, m, "User ID", "@luna:matrix.example")
	typeInto(t, m, "Allowed users", "@sol:matrix.example, @mars:matrix.example @venus:matrix.example")
	if got := shownValue(t, m, "Allowed users"); got != "@sol:matrix.example, @mars:matrix.example, @venus:matrix.example" {
		t.Errorf("allowed: %q", got)
	}
	if b.name != "" {
		t.Fatalf("saved before Save: %q", b.name)
	}
	choose(t, m, "Save", "Save")
	do(m)
	want := plugin.GatewayChange{Homeserver: "https://matrix.example", UserID: "@luna:matrix.example",
		AllowedUsers: []string{"@sol:matrix.example", "@mars:matrix.example", "@venus:matrix.example"}}
	if b.name != "matrix" || b.saved.Enabled || b.saved.Homeserver != want.Homeserver || b.saved.UserID != want.UserID ||
		!slices.Equal(b.saved.AllowedUsers, want.AllowedUsers) || b.saved.Password != "" || b.saved.Logout || m.notice != "Gateway saved" {
		t.Fatalf("saved %q %+v, notice %q", b.name, b.saved, m.notice)
	}
	choose(t, m, "Logout", "Logout")
	if do(m); !b.saved.Logout || b.saved.UserID != want.UserID {
		t.Fatalf("logout %+v", b.saved)
	}
	// Without a login and a status it says so alone.
	c.gateways[0].LoggedIn, c.gateways[0].Status = false, ""
	if got := shownValue(t, m, "Status"); got != "Not logged in" {
		t.Errorf("status: %q", got)
	}
}

// A form of its own is turned on and off, saved and logged out of from its
// rows: logging out sends its secrets empty, whatever was typed for them.
func TestGatewaySchemaSaveAndLogout(t *testing.T) {
	m, f := openSettings(t, someSettings())
	var sent []plugin.GatewayChange
	b := &gatewayBackend{fakeBackend: f, capture: func(change plugin.GatewayChange) {
		change.Values = maps.Clone(change.Values) // what is sent is cleared once it went
		sent = append(sent, change)
	}}
	m.backend = b
	c := &m.config
	c.gateways = []plugin.GatewaySettings{{Name: "custom", Title: "Custom", Status: "Ready", Fields: []plugin.GatewayField{
		{Key: "token", Label: "Bot token", Kind: "secret"},
		{Key: "region", Kind: "text", Value: "west"},
	}}}
	pick(t, m, gatewaysSection, "Custom", "Open")
	do(m)
	if got := shownValue(t, m, "Status"); got != "Not logged in · Ready" {
		t.Errorf("status: %q", got)
	}
	if got := shownValue(t, m, "Enabled"); got != "Off" {
		t.Errorf("enabled: %q", got)
	}
	choose(t, m, "Enabled", "Toggle")
	if do(m); shownValue(t, m, "Enabled") != "On" || shownValue(t, m, "region") != "west" {
		t.Errorf("toggled: %q", shownValue(t, m, "Enabled"))
	}
	if got := shownValue(t, m, "Bot token"); got != "Not set" {
		t.Errorf("secret before typing: %q", got)
	}
	typeInto(t, m, "Bot token", "t0ken")
	if got := shownValue(t, m, "Bot token"); got != "Set (hidden)" {
		t.Errorf("secret while typed: %q", got)
	}
	choose(t, m, "Save", "Save")
	do(m)
	if got := shownValue(t, m, "Bot token"); got != "Not set" {
		t.Errorf("secret after saving: %q", got)
	}
	typeInto(t, m, "Bot token", "t0ken")
	choose(t, m, "Logout", "Logout")
	do(m)
	if len(sent) != 2 {
		t.Fatalf("sent %+v", sent)
	}
	if s := sent[0]; !s.Enabled || s.Logout || s.Values["token"] != "t0ken" || s.Values["region"] != "west" {
		t.Errorf("saved %+v", s)
	}
	if s := sent[1]; !s.Enabled || !s.Logout || s.Values["token"] != "" || s.Values["region"] != "west" {
		t.Errorf("logged out %+v", s)
	}
	if c.gatewayDraft.Values["token"] != "" {
		t.Error("the draft keeps the secret")
	}
	// A refused save names the reason and shows that the typed secret is gone.
	b.err = plugin.GatewayRefusal{Reason: "log in before enabling Matrix"}
	typeInto(t, m, "Bot token", "t0ken")
	choose(t, m, "Save", "Save")
	do(m)
	if m.notice != "Gateway not saved: log in before enabling Matrix" {
		t.Errorf("notice: %q", m.notice)
	}
	if got := shownValue(t, m, "Bot token"); got != "Not set" {
		t.Errorf("secret after a refused save: %q", got)
	}
}

// Without a gateway the category says how to get one.
func TestGatewaysWithoutAny(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	if rows := m.config.gatewayRows(m); len(rows) != 1 || rows[0].label != "No gateways available" || rows[0].actions != nil {
		t.Fatalf("no gateways: %+v", rows)
	}
}
