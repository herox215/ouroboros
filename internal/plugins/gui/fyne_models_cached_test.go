//go:build cgo

package gui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"

	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/plugin"
)

// slowModels gives profile 0 of w a provider whose live list waits for
// release, and returns what it then does.
func slowModels(t *testing.T, w *Window, list func(context.Context, llm.Access) (llm.Models, error)) chan struct{} {
	t.Helper()
	release := make(chan struct{})
	provider := llmtest.Provider("two")
	provider.Models = func(ctx context.Context, a llm.Access) (llm.Models, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return llm.Models{}, ctx.Err()
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
	w.profiles[0].ctx = k.Context()
	return release
}

// remembered writes what the accesses of dir listed the last time.
func remembered(t *testing.T, dir home.Dir, models []string) {
	t.Helper()
	list, err := dir.Credentials().List()
	if err != nil || len(list) == 0 {
		t.Fatalf("accesses %v, %v", list, err)
	}
	b, _ := json.Marshal(map[string][]string{list[0].ID: models})
	if err := os.WriteFile(dir.Models(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The model page shows the models the accesses listed the last time at
// once, while they are asked anew; what they list then takes their place
// and keeps what the owner chose meanwhile.
func TestFyneModelsShowAtOnce(t *testing.T) {
	w, dirs := modelWindow(t)
	remembered(t, dirs[0], []string{"two-large", "two-old"})
	release := slowModels(t, w, llmtest.Provider("two").Models)
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	_, model := dcModelPickers(t, ui)
	waitFor(t, func() bool {
		return len(model.Options) == 3 && model.Selected == "two-small" && dcShownLabel(ui, tr("model.refreshing", nil)) != nil
	})
	chooseInSelect(t, ui, model, "two-large")
	close(release)
	waitFor(t, func() bool {
		return len(model.Options) == 2 && model.Options[1] == "two-large" && dcShownLabel(ui, tr("model.refreshing", nil)) == nil
	})
	fyne.DoAndWait(func() {
		if model.Selected != "two-large" {
			t.Errorf("the choice of the owner went: %q", model.Selected)
		}
		if model.Options[0] != "two-small" {
			t.Errorf("models %q", model.Options)
		}
	})
}

// An access never listed shows that its models are on their way, not that
// it has none, and the page takes them once they come.
func TestFyneModelsNeverListed(t *testing.T) {
	w, dirs := modelWindow(t)
	os.Remove(dirs[0].Models())
	release := slowModels(t, w, llmtest.Provider("two").Models)
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	waitFor(t, func() bool { return dcShownLabel(ui, tr("model.refreshing", nil)) != nil })
	close(release)
	_, model := dcModelPickers(t, ui)
	waitFor(t, func() bool { return len(model.Options) == 2 && !model.Disabled() })
}

// When asking the accesses anew fails, the models listed before stay, and
// the page says what went wrong.
func TestFyneModelsRefreshFails(t *testing.T) {
	w, dirs := modelWindow(t)
	remembered(t, dirs[0], []string{"two-small", "two-large"})
	release := slowModels(t, w, func(context.Context, llm.Access) (llm.Models, error) {
		// The configuration breaks while the models are listed.
		if err := os.WriteFile(filepath.Join(string(dirs[0]), "config.json"), []byte("{"), 0o600); err != nil {
			return llm.Models{}, err
		}
		return llm.Models{}, errors.New("down")
	})
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	_, model := dcModelPickers(t, ui)
	waitFor(t, func() bool { return len(model.Options) == 2 })
	close(release)
	waitFor(t, func() bool { return dcShownLabel(ui, tr("dialog.failed", nil)) != nil })
	fyne.DoAndWait(func() {
		if len(model.Options) != 2 {
			t.Errorf("the models listed before went: %q", model.Options)
		}
	})
}

// A list that comes while a choice is saved leaves the page as it is.
func TestFyneModelsRefreshWhileSaving(t *testing.T) {
	w, _ := modelWindow(t)
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() {
		d := &fyneModel{ui: ui}
		d.build()
		d.page()
		d.loaded(ModelSelection{Accesses: []ModelAccess{{ID: "a", Label: "A", Models: []string{"m"}}}, Access: "a", Model: "m"}, nil)
		d.saving = true
		d.refreshed(ModelSelection{Accesses: []ModelAccess{{ID: "a", Label: "A", Models: []string{"m", "n"}}}, Access: "a", Model: "m"}, nil)
		if len(d.model.Options) != 1 {
			t.Errorf("saving took the new list: %q", d.model.Options)
		}
	})
}
