package tui

import (
	"ouroboros/plugin"
	"strings"
	"testing"
)

func TestGatewayVerificationOwnerActions(t *testing.T) {
	m, f := openSettings(t, someSettings())
	b := &gatewayBackend{fakeBackend: f}
	m.backend = b
	c := &m.config
	c.gateway = "matrix"
	c.gatewayDraft = plugin.GatewayChange{Password: "unsaved-secret", Values: map[string]string{"password": "other-secret"}}
	v := &plugin.GatewayVerification{Stage: "comparing", TransactionID: "displayed-txn", DeviceID: "TRUSTED", Emojis: []string{"🐶", "🐱", "🦁", "🐎", "🦄", "🐷", "🐘"}}
	c.gateways = []plugin.GatewaySettings{{Name: "matrix", Enabled: true, LoggedIn: true, Verification: v}}
	// The controls are present in both legacy and schema-driven settings forms.
	for _, fields := range [][]plugin.GatewayField{nil, {{Key: "homeserver", Kind: "text"}}} {
		c.gateways[0].Fields = fields
		var found bool
		for _, r := range c.gatewayPage(m) {
			if r.label == "Compare all emojis" {
				found = true
			}
		}
		if !found {
			t.Fatal("verification missing from settings form")
		}
	}
	rows := c.gatewayVerificationRows(m)
	var confirmed bool
	for _, r := range rows {
		if r.label == "Compare all emojis" {
			if len(r.details) != 7 || r.details[6].name != "7" || r.details[6].value != "🐘" {
				t.Fatal("ordered emojis not visible on separate lines")
			}
			if !strings.Contains(r.value, "🐘") {
				t.Fatal("missing emoji")
			}
			msg := r.actions[0].run(m)().(changedMsg)
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			confirmed = true
		}
	}
	if !confirmed || b.name != "matrix" || b.saved.VerificationAction != "confirm" || b.saved.VerificationTransactionID != "displayed-txn" {
		t.Fatalf("wrong action: %+v", b.saved)
	}
	if b.saved.Password != "" || b.saved.Values != nil || c.gatewayDraft.Password != "unsaved-secret" {
		t.Fatal("verification submitted or replaced unsaved credentials")
	}
	v.Stage = "requested"
	for _, r := range c.gatewayVerificationRows(m) {
		if r.label == "Compare all emojis" {
			t.Fatal("confirmation before SAS")
		}
	}
	v.Stage = "comparing"
	v.Emojis = v.Emojis[:6]
	for _, r := range c.gatewayVerificationRows(m) {
		if r.label == "Compare all emojis" {
			t.Fatal("confirmation with incomplete SAS")
		}
	}
	v.Stage, v.TransactionID = "idle", ""
	for _, r := range c.gatewayVerificationRows(m) {
		for _, a := range r.actions {
			if a.name == "Start" {
				a.run(m)()
				if b.saved.VerificationAction != "start" || b.saved.VerificationTransactionID != "" {
					t.Fatal("bad start")
				}
			}
		}
	}
	b.fail = true
	msg := c.gatewayVerificationAction(m, "cancel", "txn")().(changedMsg)
	if msg.err == nil || strings.Contains(msg.err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", msg.err)
	}
}
