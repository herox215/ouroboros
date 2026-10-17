package gui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

func TestWindowGateways(t *testing.T) {
	w, _ := modelWindow(t)
	var received [2]plugin.GatewayChange
	for i := range 2 {
		k := kernel.New()
		if err := k.Start(plugin.New("gateway", func(ctx *plugin.Context, _ struct{}) error {
			plugin.Add(ctx, plugin.Gateway{Name: "fixture", Title: "Fixture", Settings: func(context.Context) (plugin.GatewaySettings, error) {
				return plugin.GatewaySettings{Enabled: received[i].Enabled, UserID: received[i].UserID, LoggedIn: received[i].Password != ""}, nil
			}, Save: func(_ context.Context, c plugin.GatewayChange) error { received[i] = c; return nil }})
			return nil
		})); err != nil {
			t.Fatal(err)
		}
		w.profiles[i].ctx = k.Context()
	}
	if err := w.SaveGateway(1, "fixture", plugin.GatewayChange{Enabled: true, UserID: "@owner:example", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	if received[0].Enabled || received[1].Password != "secret" {
		t.Fatal("wrong profile")
	}
	got, err := w.Gateways(1)
	if err != nil || len(got) != 1 || got[0].Name != "fixture" || !got[0].LoggedIn {
		t.Fatalf("%+v %v", got, err)
	}
	snapshot, _ := json.Marshal(w.InitialState())
	public, _ := json.Marshal(got)
	if strings.Contains(string(snapshot)+string(public), "secret") {
		t.Fatal("credential exposed")
	}
	if err := w.SaveGateway(1, "missing", plugin.GatewayChange{}); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := w.Gateways(-1); err == nil {
		t.Fatal("invalid profile accepted")
	}
	if err := w.SaveGateway(2, "fixture", plugin.GatewayChange{}); err == nil {
		t.Fatal("invalid profile accepted")
	}
	w.cancel()
	if _, err := w.Gateways(1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

// A profile without plugins has no gateways, a gateway without settings is
// left out, and settings that cannot be read are an error.
func TestWindowGatewaySettings(t *testing.T) {
	w, _ := modelWindow(t)
	w.profiles[0].ctx = nil
	if got, err := w.Gateways(0); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("without plugins: %#v %v", got, err)
	}
	sentinel := errors.New("settings unreadable")
	for _, gateways := range [][]plugin.Gateway{
		{{Name: "silent", Title: "Silent"}, {Name: "fixture", Title: "Fixture", Settings: func(context.Context) (plugin.GatewaySettings, error) {
			return plugin.GatewaySettings{Enabled: true}, nil
		}}},
		{{Name: "broken", Title: "Broken", Settings: func(context.Context) (plugin.GatewaySettings, error) {
			return plugin.GatewaySettings{}, sentinel
		}}},
	} {
		k := kernel.New()
		if err := k.Start(plugin.New("gateway", func(ctx *plugin.Context, _ struct{}) error {
			for _, g := range gateways {
				plugin.Add(ctx, g)
			}
			return nil
		})); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k.Stop)
		w.profiles[1].ctx = k.Context()
		got, err := w.Gateways(1)
		if gateways[0].Name == "broken" {
			if !errors.Is(err, sentinel) || got != nil {
				t.Fatalf("unreadable settings: %+v %v", got, err)
			}
			continue
		}
		if err != nil || len(got) != 1 || got[0].Name != "fixture" || got[0].Title != "Fixture" || !got[0].Enabled {
			t.Fatalf("gateway without settings: %+v %v", got, err)
		}
	}
}
