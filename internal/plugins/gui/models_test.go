package gui

import (
	"encoding/json"
	"errors"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/kernel"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/plugin"
)

func modelWindow(t *testing.T) (*Window, []home.Dir) {
	t.Helper()
	var profiles []*Profile
	var dirs []home.Dir
	for range 2 {
		dir := home.Dir(t.TempDir())
		access, err := dir.Credentials().Put(credentials.Access{Provider: "two", Name: "Account", Secret: json.RawMessage(`{"token":"good"}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := dir.SaveConfig(config.Config{Access: access.ID, Model: "two-small", Name: "Test", PrivateKey: identity.Generate().Private()}); err != nil {
			t.Fatal(err)
		}
		k := kernel.New()
		var p *Profile
		providers := plugin.New("models", func(ctx *plugin.Context, _ struct{}) error {
			plugin.Add(ctx, llmtest.Provider("two"))
			return nil
		})
		if err := k.Start(sessionProvider(&testSessions{}), providers, Plugin(plugin.OwnerOf(dir, nil), func(profile *Profile) { p = profile })); err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, p)
		dirs = append(dirs, dir)
	}
	Together(profiles)
	page, err := profiles[0].page()
	if err != nil {
		t.Fatal(err)
	}
	w := newWindow(page)
	t.Cleanup(w.Close)
	return w, dirs
}

func TestWindowModels(t *testing.T) {
	w, dirs := modelWindow(t)
	selection, err := w.Models(1)
	if err != nil || len(selection.Accesses) != 1 || selection.Model != "two-small" || selection.Access != selection.Accesses[0].ID || len(selection.Accesses[0].Models) != 2 || selection.Accesses[0].Label != "Account" {
		t.Fatalf("models: %+v, %v", selection, err)
	}
	access := selection.Access
	if switched, err := w.SetModel(1, access, " two-large "); err != nil || switched {
		t.Fatalf("set: %v, %v", switched, err)
	}
	for i, dir := range dirs {
		cfg, err := dir.Config()
		if err != nil {
			t.Fatal(err)
		}
		want := "two-small"
		if i == 1 {
			want = "two-large"
		}
		if cfg.Model != want {
			t.Fatalf("profile %d: %q", i, cfg.Model)
		}
	}
	for _, invalid := range []int{-1, 2} {
		if _, err := w.Models(invalid); err == nil {
			t.Fatal("invalid index accepted")
		}
		if _, err := w.SetModel(invalid, access, "two-small"); err == nil {
			t.Fatal("invalid index accepted")
		}
	}
	for _, pick := range [][2]string{{"missing", "two-small"}, {access, " "}} {
		if _, err := w.SetModel(1, pick[0], pick[1]); err == nil {
			t.Fatalf("invalid pick accepted: %v", pick)
		}
	}
	// A failed provider retains its cached list and a displayable error.
	saved, err := dirs[1].Credentials().Get(access)
	if err != nil {
		t.Fatal(err)
	}
	saved.Secret = json.RawMessage(`{"token":"good-down"}`)
	if _, err := dirs[1].Credentials().Put(saved); err != nil {
		t.Fatal(err)
	}
	selection, err = w.Models(1)
	if err != nil || len(selection.Accesses[0].Models) != 2 || selection.Accesses[0].Error == "" {
		t.Fatalf("failed access: %+v, %v", selection, err)
	}
	// An unconfigured profile returns the Owner sentinel rather than starting it.
	if err := dirs[0].SaveConfig(config.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Models(0); !errors.Is(err, plugin.ErrNotSetUp) {
		t.Fatalf("not set up: %v", err)
	}
}
