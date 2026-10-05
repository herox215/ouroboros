package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// /model opens the model screen, lists the models of the provider with the
// one in use marked, and enter saves the one selected.
func TestModelScreenChoosesAModel(t *testing.T) {
	m, b := newModel(t)
	b.status.Model = "gpt-4o"
	m.Update(m.refreshStatus())
	b.offers = Models{Chat: []string{"gpt-4o", "gpt-4o-mini", "o3-mini"}, Default: "gpt-4o"}
	cmd := run(m, "/model")
	if m.screen != screenModel || m.modelBusy == "" || m.input.Value() != "" {
		t.Fatal("/model should open the model screen and list the models")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "listing models") {
		t.Fatalf("while the models are on their way:\n%s", view)
	}
	deliver(m, cmd)
	if m.modelBusy != "" || m.modelSel != 0 || m.modelErr != nil {
		t.Fatalf("the models should be in: sel %d, err %v", m.modelSel, m.modelErr)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "▸ gpt-4o  in use") {
		t.Fatalf("the model in use should be marked:\n%s", view)
	}

	// Down to the second model, enter saves it.
	press(m, "down")
	m.Update(press(m, "enter")())
	if len(b.set) != 1 || b.set[0] != "gpt-4o-mini" || m.notice != "model changed to gpt-4o-mini" || m.noticeErr {
		t.Fatalf("set %v, notice %q", b.set, m.notice)
	}
	if m.screen != screenModel {
		t.Fatal("the screen should stay open")
	}
	// The status is read anew, and marks the new model as the one in use.
	b.status.Model = "gpt-4o-mini"
	m.Update(m.refreshStatus())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "▸ gpt-4o-mini  in use") || strings.Contains(view, "gpt-4o  in use") {
		t.Fatalf("the new model should be marked:\n%s", view)
	}

	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc should go back to the chat")
	}
}

// An error of the provider is shown in place of the list, and enter does
// nothing then.
func TestModelScreenWithoutModels(t *testing.T) {
	m, b := newModel(t)
	b.modelsErr = errors.New("cannot list models at https://api.example.com: no route")
	deliver(m, run(m, "/model"))
	if m.modelErr == nil || m.modelBusy != "" {
		t.Fatalf("the error should be in: %q", m.modelErr)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✗ cannot list models") {
		t.Fatalf("the error should be shown:\n%s", view)
	}
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("saving without models should do nothing")
	}
	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc should go back to the chat")
	}
}

// A model that cannot be saved is no error to pretend: the screen says why.
func TestModelSaveThatFails(t *testing.T) {
	m, b := newModel(t)
	b.offers = Models{Chat: []string{"gpt-4o"}, Default: "gpt-4o"}
	b.fail = map[string]error{"setmodel": errors.New("disk full")}
	deliver(m, run(m, "/model"))
	m.Update(press(m, "enter")())
	if m.notice != "disk full" || !m.noticeErr {
		t.Fatalf("a model that cannot be saved: %q", m.notice)
	}
}

// A node that is not set up has no provider to ask for its models.
func TestModelWithoutSetup(t *testing.T) {
	m, b := newModel(t)
	b.status = Status{}
	m.Update(m.refreshStatus())
	if run(m, "/model"); m.screen == screenModel || m.notice != "set up the node first: /setup" || !m.noticeErr {
		t.Fatalf("a node without a setup: %q", m.notice)
	}
}
