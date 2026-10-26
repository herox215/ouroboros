package gui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/kernel"
	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/memory"
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

// Models need the owner of the profile, and the selected model of a
// configuration that can be read.
func TestWindowModelsFailures(t *testing.T) {
	if _, err := sessionWindow(t, &testSessions{}).Models(0); err == nil || !strings.Contains(err.Error(), "has no owner access") {
		t.Fatalf("no owner: %v", err)
	}
	w, dirs := modelWindow(t)
	provider := llmtest.Provider("two")
	list := provider.Models
	provider.Models = func(ctx context.Context, a llm.Access) (llm.Models, error) {
		// The configuration breaks while the models are listed.
		if err := os.WriteFile(filepath.Join(string(dirs[1]), "config.json"), []byte("{"), 0o600); err != nil {
			return llm.Models{}, err
		}
		return list(ctx, a)
	}
	k := kernel.New()
	if err := k.Start(plugin.New("models", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, provider)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	w.profiles[1].ctx = k.Context()
	if _, err := w.Models(1); err == nil || !strings.HasPrefix(err.Error(), "read selected model: ") {
		t.Fatalf("unreadable selection: %v", err)
	}
}

// A memory the review kept comes with what takes it back, and the window
// takes it back for the profile it is of.
func TestWindowUndoesRemembered(t *testing.T) {
	w, dirs := modelWindow(t)
	store, err := memory.Open(dirs[1].History())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, was, err := store.Remember(context.Background(), "", "style", "short answers", "c", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(plugin.Remembered{Memory: e, Was: was})
	shown := (&Profile{}).event(plugin.SessionEvent{Kind: plugin.SessionRemembered, Text: "style: short answers", Args: args})
	if shown.Remembered != string(args) || shown.Text != "style: short answers" {
		t.Fatalf("event %+v", shown)
	}
	if err := w.UndoRemembered(7, shown.Remembered); err == nil {
		t.Fatal("taken back for no profile")
	}
	if err := w.UndoRemembered(1, shown.Remembered); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "", e.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("not taken back: %v", err)
	}
}
