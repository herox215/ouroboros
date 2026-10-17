package tui

import (
	"cmp"
	"maps"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"ouroboros/plugin"
)

func (c *settingsScreen) gatewayRows(m *Model) []row {
	out := []row{}
	for _, g := range c.gateways {
		out = append(out, row{label: cmp.Or(g.Title, g.Name), value: g.Status, actions: []action{{"Open", func(m *Model) tea.Cmd {
			c.back = append(c.back, c.sel)
			c.gateway = g.Name
			c.gatewayDraft = plugin.GatewayChange{Enabled: g.Enabled, Homeserver: g.Homeserver, UserID: g.UserID, AllowedUsers: append([]string{}, g.AllowedUsers...)}
			if len(g.Fields) > 0 {
				c.gatewayDraft = plugin.GatewayChange{Enabled: g.Enabled, Values: map[string]string{}, Lists: map[string][]string{}}
				for _, f := range g.Fields {
					switch f.Kind {
					case "secret":
						c.gatewayDraft.Values[f.Key] = ""
					case "list":
						c.gatewayDraft.Lists[f.Key] = append([]string{}, f.List...)
					case "bool":
						c.gatewayDraft.Values[f.Key] = strconv.FormatBool(f.On)
					default:
						c.gatewayDraft.Values[f.Key] = f.Value
					}
				}
			}
			c.sel, m.choice, c.top = 0, 0, 0
			return nil
		}}}})
	}
	if len(out) == 0 {
		out = append(out, row{label: "No gateways available", value: "Enable a gateway plugin and restart."})
	}
	return out
}

func (c *settingsScreen) gatewayEdit(m *Model, label, value string, secret bool, submit func(string) tea.Cmd) tea.Cmd {
	cmd := c.startEdit(m, label, value, nil)
	c.editing.submit = submit
	if secret {
		c.input.EchoMode = textinput.EchoPassword
	}
	return cmd
}

func (c *settingsScreen) saveGateway(m *Model, password string, logout bool) tea.Cmd {
	name, change := c.gateway, c.gatewayDraft
	change.Values = maps.Clone(change.Values)
	change.Password, change.Logout = password, logout
	secretKeys := []string{}
	for _, f := range c.gatewayFields() {
		if f.Kind == "secret" {
			secretKeys = append(secretKeys, f.Key)
			if logout {
				change.Values[f.Key] = ""
			}
		}
	}
	c.clearGatewaySecrets()
	c.input.SetValue("")
	backend, ctx := m.backend, m.ctx
	return func() tea.Msg {
		err := backend.SaveGateway(ctx, name, change)
		change.Password = ""
		for _, key := range secretKeys {
			change.Values[key] = ""
		}
		// Contributions may return arbitrary errors: never display credential-bearing errors.
		if err != nil {
			return changedMsg{done: "Gateway could not be saved", err: gatewaySaveError{}}
		}
		return changedMsg{done: "Gateway saved"}
	}
}

type gatewaySaveError struct{}

func (gatewaySaveError) Error() string {
	return "Gateway could not be saved; check the settings and try again"
}

func (c *settingsScreen) gatewayPage(m *Model) []row {
	d := &c.gatewayDraft
	editRow := func(label, value string, assign func(string)) row {
		return row{label: label, value: value, actions: []action{{"Edit", func(m *Model) tea.Cmd {
			return c.gatewayEdit(m, label, value, false, func(v string) tea.Cmd { assign(v); return nil })
		}}}}
	}
	status := "Not logged in"
	for _, g := range c.gateways {
		if g.Name == c.gateway {
			if g.LoggedIn {
				status = "Logged in"
			}
			if g.Status != "" {
				status += " · " + g.Status
			}
		}
	}
	enabled := "Off"
	if d.Enabled {
		enabled = "On"
	}
	if fields := c.gatewayFields(); len(fields) > 0 {
		out := []row{
			{label: "Status", value: status},
			{label: "Enabled", value: enabled, actions: []action{{"Toggle", func(m *Model) tea.Cmd { d.Enabled = !d.Enabled; return nil }}}},
		}
		for _, f := range fields {
			label, value := cmp.Or(f.Label, f.Key), d.Values[f.Key]
			switch f.Kind {
			case "bool":
				out = append(out, row{label: label, value: value, actions: []action{{"Toggle", func(m *Model) tea.Cmd { d.Values[f.Key] = strconv.FormatBool(d.Values[f.Key] != "true"); return nil }}}})
			case "secret":
				out = append(out, row{label: label, value: "Hidden (write-only)", actions: []action{{"Edit", func(m *Model) tea.Cmd {
					return c.gatewayEdit(m, label, "", true, func(v string) tea.Cmd { d.Values[f.Key] = v; return nil })
				}}}})
			case "list":
				out = append(out, editRow(label, strings.Join(d.Lists[f.Key], ", "), func(v string) { d.Lists[f.Key] = gatewayList(v) }))
			default:
				out = append(out, editRow(label, value, func(v string) { d.Values[f.Key] = v }))
			}
		}
		out = append(out, c.gatewayVerificationRows(m)...)
		return append(out,
			row{label: "Access", value: gatewayAccess},
			row{label: "Save", actions: []action{{"Save", func(m *Model) tea.Cmd { return c.saveGateway(m, "", false) }}}},
			row{label: "Logout", actions: []action{{"Logout", func(m *Model) tea.Cmd { return c.saveGateway(m, "", true) }}}})
	}
	out := []row{
		{label: "Status", value: status},
		{label: "Enabled", value: enabled, actions: []action{{"Toggle", func(m *Model) tea.Cmd { d.Enabled = !d.Enabled; return nil }}}},
		editRow("Homeserver", d.Homeserver, func(v string) { d.Homeserver = v }),
		editRow("User ID", d.UserID, func(v string) { d.UserID = v }),
		{label: "Password", value: "Not stored in the UI", actions: []action{{"Login / Save", func(m *Model) tea.Cmd {
			return c.gatewayEdit(m, "Password", "", true, func(v string) tea.Cmd { return c.saveGateway(m, v, false) })
		}}}},
		editRow("Allowed users", strings.Join(d.AllowedUsers, ", "), func(v string) {
			d.AllowedUsers = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })
		}),
		{label: "Access", value: gatewayAccess},
		{label: "Save", actions: []action{{"Save", func(m *Model) tea.Cmd { return c.saveGateway(m, "", false) }}}},
		{label: "Logout", actions: []action{{"Logout", func(m *Model) tea.Cmd { return c.saveGateway(m, "", true) }}}},
	}
	return append(out, c.gatewayVerificationRows(m)...)
}

// Verification actions never submit the connection form or its write-only secrets.
func (c *settingsScreen) gatewayVerificationRows(m *Model) []row {
	for _, g := range c.gateways {
		if g.Name != c.gateway || g.Verification == nil {
			continue
		}
		v := *g.Verification
		out := []row{{label: "Device verification", value: v.Stage + " · " + v.Message, actions: []action{{"Refresh", func(m *Model) tea.Cmd { return c.reload(m) }}}}}
		if v.TransactionID != "" {
			out = append(out, row{label: "Verification transaction", value: v.TransactionID})
		}
		if v.DeviceID != "" {
			out = append(out, row{label: "Other device", value: v.DeviceID})
		}
		if v.Stage == "comparing" && v.TransactionID != "" && len(v.Emojis) == 7 {
			ordered := make([]detail, len(v.Emojis))
			for i, emoji := range v.Emojis {
				ordered[i] = detail{name: strconv.Itoa(i + 1), value: emoji}
			}
			out = append(out, row{label: "Compare all emojis", value: strings.Join(v.Emojis, "  "), details: ordered, actions: []action{{"Emojis match", func(m *Model) tea.Cmd { return c.gatewayVerificationAction(m, "confirm", v.TransactionID) }}}})
		}
		if v.TransactionID != "" && (v.Stage == "requested" || v.Stage == "ready" || v.Stage == "comparing" || v.Stage == "confirmed") {
			out = append(out, row{label: "Cancel verification", actions: []action{{"Cancel", func(m *Model) tea.Cmd { return c.gatewayVerificationAction(m, "cancel", v.TransactionID) }}}})
		} else if g.Enabled && g.LoggedIn {
			out = append(out, row{label: "Verify with a trusted session of the same Matrix account", actions: []action{{"Start", func(m *Model) tea.Cmd { return c.gatewayVerificationAction(m, "start", "") }}}})
		}
		return out
	}
	return nil
}

func (c *settingsScreen) gatewayVerificationAction(m *Model, action, transaction string) tea.Cmd {
	name, backend, ctx := c.gateway, m.backend, m.ctx
	return func() tea.Msg {
		err := backend.SaveGateway(ctx, name, plugin.GatewayChange{VerificationAction: action, VerificationTransactionID: transaction})
		if err != nil {
			return changedMsg{done: "Verification action failed", err: gatewaySaveError{}}
		}
		return changedMsg{done: "Verification action queued; refresh to see progress"}
	}
}

const gatewayAccess = "Allowlisted users are delegated this profile’s agent tools, INCLUDING permission answers, not just chat. Empty denies all."

func gatewayList(v string) []string {
	out := []string{}
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func (c *settingsScreen) gatewayFields() []plugin.GatewayField {
	for _, g := range c.gateways {
		if g.Name == c.gateway {
			return g.Fields
		}
	}
	return nil
}
func (c *settingsScreen) clearGatewaySecrets() {
	if c.input.EchoMode == textinput.EchoPassword {
		c.input.SetValue("")
	}
	c.gatewayDraft.Password = ""
	for _, f := range c.gatewayFields() {
		if f.Kind == "secret" {
			c.gatewayDraft.Values[f.Key] = ""
		}
	}
}
