package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// local and plan are two accesses of a node: an API on this machine, which is
// in use, and a plan of another provider.
func local(models ...string) Models {
	return Models{Access: plugin.SetupAccess{ID: "a1", Provider: "openai", Label: "Ollama local", About: "http://localhost:11434/v1", Active: true}, Models: models}
}

func plan(models ...string) Models {
	return Models{Access: plugin.SetupAccess{ID: "b2", Provider: "chatgpt", Label: "OpenCode Go", About: "me@example.com"}, Models: models}
}

// /model opens the model screen, lists the models of every access below what
// the access is called, with the one in use marked, and enter saves the one
// selected.
func TestModelScreenChoosesAModel(t *testing.T) {
	m, b := newModel(t)
	b.status.Model = "gpt-4o"
	m.Update(m.refreshStatus())
	b.offers = []Models{local("gpt-4o", "gpt-4o-mini", "o3-mini")}
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
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Ollama local · http://localhost:11434/v1") || !strings.Contains(view, "▸ gpt-4o  in use") {
		t.Fatalf("the access and the model in use should show:\n%s", view)
	}

	// Down to the second model, enter saves it. Up does not go past the first.
	press(m, "up")
	press(m, "down")
	press(m, "up")
	if m.modelSel != 0 {
		t.Fatalf("up should select the first model: %d", m.modelSel)
	}
	press(m, "down")
	m.Update(press(m, "enter")())
	if len(b.set) != 1 || b.set[0] != "a1 gpt-4o-mini" || m.notice != "model changed to gpt-4o-mini" || m.noticeErr {
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

// The models of all accesses are in one list, grouped by access. A model of
// another access is picked like any other: that switches the provider, and
// the owner is told once what that means for the cache.
func TestModelScreenSwitchesTheProvider(t *testing.T) {
	m, b := newModel(t)
	b.status.Model = "llama3"
	m.Update(m.refreshStatus())
	// Both accesses have a model of the same name: only the one of the
	// access in use is the one in use.
	b.offers = []Models{plan("gpt-6", "llama3"), local("llama3", "qwen")}
	deliver(m, run(m, "/model"))
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"OpenCode Go · me@example.com", "  gpt-6", "  llama3 ", "Ollama local · http://localhost:11434/v1", "▸ llama3  in use", "3 of 4"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	if strings.Count(view, "in use") != 1 || m.modelSel != 2 {
		t.Fatalf("selected %d, in use:\n%s", m.modelSel, view)
	}
	if strings.Index(view, "OpenCode Go") > strings.Index(view, "gpt-6") || strings.Index(view, "gpt-6") > strings.Index(view, "Ollama local") {
		t.Fatalf("the models are not below their access:\n%s", view)
	}

	// Up into the other access, and enter: saved with the access it is of.
	press(m, "up")
	b.switched = true
	m.Update(press(m, "enter")())
	if len(b.set) != 1 || b.set[0] != "b2 llama3" {
		t.Fatalf("set %v", b.set)
	}
	if want := "model changed to llama3 of OpenCode Go · " + switchedNote; m.notice != want || m.noticeErr {
		t.Fatalf("notice %q, want %q", m.notice, want)
	}
	if !strings.Contains(switchedNote, "prompt cache starts anew") || !strings.Contains(switchedNote, "cost more or take longer") {
		t.Fatalf("the note does not say what the switch means: %q", switchedNote)
	}
	// Down does not go past the last model.
	press(m, "down", "down", "down", "down", "j")
	if m.modelSel != 3 {
		t.Fatalf("selected %d", m.modelSel)
	}
	press(m, "k")
	if m.modelSel != 2 {
		t.Fatalf("selected %d", m.modelSel)
	}
}

// An access that does not answer shows with why, below what it is called,
// and does not keep the models of the others back. What it listed the last
// time can still be picked.
func TestModelScreenWithAnAccessThatIsDown(t *testing.T) {
	m, b := newModel(t)
	down, never := plan("gpt-6"), plan()
	down.Err, never.Err = "no route to host", "API key rejected"
	never.Access.ID, never.Access.Label, never.Access.About = "c3", "Work", ""
	b.offers = []Models{down, never, local("llama3")}
	deliver(m, run(m, "/model"))
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"✗ no route to host – the models it listed the last time:", "▸ gpt-6", "Work ", "✗ API key rejected ", "llama3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	if len(m.choices) != 2 || strings.Contains(view, "Work ·") {
		t.Fatalf("choices %+v:\n%s", m.choices, view)
	}
	m.Update(press(m, "enter")())
	if len(b.set) != 1 || b.set[0] != "b2 gpt-6" {
		t.Fatalf("set %v", b.set)
	}
}

// Of many models the ones around the selected show, with the access they
// belong to at the top.
func TestModelScreenScrolls(t *testing.T) {
	m, b := newModel(t)
	var many []string
	for i := range 40 {
		many = append(many, fmt.Sprintf("model-%02d", i))
	}
	b.offers = []Models{plan("gpt-6"), local(many...)}
	deliver(m, run(m, "/model"))
	for range 30 {
		press(m, "down")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "▸ model-29") || strings.Contains(view, "gpt-6") || strings.Contains(view, "model-00") || !strings.Contains(view, "31 of 41") {
		t.Fatalf("around the selected model:\n%s", view)
	}
	if !strings.Contains(view, "Ollama local · http://localhost:11434/v1") || strings.Contains(view, "OpenCode Go") {
		t.Fatalf("the access of the models shown should stay in sight:\n%s", view)
	}
	// A list that starts with the line of an access keeps it.
	m.modelSel = 0
	lines := m.modelList(80, 3)
	if !strings.Contains(ansi.Strip(lines[0]), "OpenCode Go") {
		t.Fatalf("the top of the list: %q", lines)
	}
	// One line is the model selected, not the name of its access.
	m.modelSel = 5
	if lines = m.modelList(80, 1); !strings.Contains(ansi.Strip(lines[0]), "▸ model-04") {
		t.Fatalf("one line: %q", lines)
	}
}

// An error of the node is shown in place of the list, and enter does
// nothing then.
func TestModelScreenWithoutModels(t *testing.T) {
	m, b := newModel(t)
	b.modelsErr = errors.New("the credentials cannot be read")
	deliver(m, run(m, "/model"))
	if m.modelErr == nil || m.modelBusy != "" {
		t.Fatalf("the error should be in: %q", m.modelErr)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✗ the credentials cannot be read") {
		t.Fatalf("the error should be shown:\n%s", view)
	}
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("saving without models should do nothing")
	}
	// Accesses that list nothing leave nothing to pick.
	m.modelsLoaded(modelsMsg{models: []Models{plan()}})
	if cmd := press(m, "enter", "down", "up"); cmd != nil || m.modelSel != 0 {
		t.Fatal("picking without models should do nothing")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "0 of 0") {
		t.Fatalf("no models:\n%s", view)
	}
	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc should go back to the chat")
	}
}

// A model that cannot be saved is no error to pretend: the screen says why.
func TestModelSaveThatFails(t *testing.T) {
	m, b := newModel(t)
	b.offers = []Models{local("gpt-4o")}
	b.fail = map[string]error{"setmodel": errors.New("disk full")}
	deliver(m, run(m, "/model"))
	m.Update(press(m, "enter")())
	if m.notice != "disk full" || !m.noticeErr {
		t.Fatalf("a model that cannot be saved: %q", m.notice)
	}
}

// A node that is not set up has no access to ask for its models.
func TestModelWithoutSetup(t *testing.T) {
	m, b := newModel(t)
	b.status = Status{}
	m.Update(m.refreshStatus())
	if run(m, "/model"); m.screen == screenModel || m.notice != "set up the node first: /setup" || !m.noticeErr {
		t.Fatalf("a node without a setup: %q", m.notice)
	}
}

// Enter picks nothing while the models are on their way or did not come,
// whatever was listed before.
func TestModelScreenPicksOnlyWhatIsListed(t *testing.T) {
	m, b := newModel(t)
	b.offers = []Models{local("gpt-4o")}
	deliver(m, run(m, "/model"))
	m.modelBusy = "listing models"
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("picked while the models are listed")
	}
	m.modelBusy, m.modelErr = "", errors.New("gone")
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("picked from a list that did not come")
	}
	m.modelErr = nil
	if cmd := press(m, "enter"); cmd == nil || len(b.set) != 0 {
		t.Fatal("a model that is listed should be picked")
	}
}

// The list is laid out line by line: an access, why it did not answer, its
// models, and an empty line before the next access. Each line is cut to the
// width there is.
func TestModelListLayout(t *testing.T) {
	m, _ := newModel(t)
	down := plan("gpt-6")
	down.Err = "no route to host"
	never := plan()
	never.Err, never.Access.Label, never.Access.About = "API key rejected", "Work", ""
	m.modelsLoaded(modelsMsg{models: []Models{down, never, local("llama3")}})
	var got []string
	for _, l := range m.modelList(80, 20) {
		got = append(got, ansi.Strip(l))
	}
	want := []string{
		"OpenCode Go · me@example.com",
		"✗ no route to host – the models it listed the last time:",
		"▸ gpt-6",
		"",
		"Work",
		"✗ API key rejected",
		"",
		"Ollama local · http://localhost:11434/v1",
		"  llama3",
		"",
		"1 of 2",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// In a narrow window every line is cut where its room ends: the name of
	// an access 4 cells, a reason 6 and a model 14 before the edge.
	long := local(strings.Repeat("m", 60))
	long.Err = strings.Repeat("e", 60)
	m.modelsLoaded(modelsMsg{models: []Models{long}})
	lines := m.modelList(40, 20)
	for i, width := range []int{36, 34, 28} {
		if w := ansi.StringWidth(ansi.Strip(lines[i])); w != width || !strings.HasSuffix(ansi.Strip(lines[i]), "…") {
			t.Errorf("line %d is %d cells wide, want %d: %q", i, w, width, ansi.Strip(lines[i]))
		}
	}
}

// The list shows the lines around the model selected: half of them before
// it, and never past the end.
func TestModelListWindow(t *testing.T) {
	m, _ := newModel(t)
	var many []string
	for i := range 40 {
		many = append(many, fmt.Sprintf("model-%02d", i))
	}
	m.modelsLoaded(modelsMsg{models: []Models{local(many...)}})
	shown := func(sel int) []string {
		m.modelSel = sel
		var lines []string
		for _, l := range m.modelList(80, 10) {
			lines = append(lines, strings.TrimSpace(ansi.Strip(l)))
		}
		return lines[:10]
	}
	// In the middle: the access at the top, then from five lines before
	// the model selected on.
	if got := shown(20); got[0] != "Ollama local · http://localhost:11434/v1" || got[1] != "model-16" || got[5] != "▸ model-20" || got[9] != "model-24" {
		t.Fatalf("around model-20: %q", got)
	}
	// At the start nothing is before the access, at the end nothing after
	// the last model.
	if got := shown(0); got[0] != "Ollama local · http://localhost:11434/v1" || got[1] != "▸ model-00  in use" && got[1] != "▸ model-00" || got[9] != "model-08" {
		t.Fatalf("at the start: %q", got)
	}
	if got := shown(39); got[1] != "model-31" || got[9] != "▸ model-39" {
		t.Fatalf("at the end: %q", got)
	}
}

// p on the model screen makes the model selected what peers are answered
// with, which the list marks, and p on that one lets them be answered with
// the model of the node again. Nothing of it changes the model of the node.
func TestModelScreenChoosesAModelForPeers(t *testing.T) {
	m, b := newModel(t)
	b.status.Model = "gpt-4o"
	b.offers = []Models{local("gpt-4o", "gpt-4o-mini"), plan("gpt-4o-mini")}
	// p picks for peers, and the status is read anew as the UI does then.
	p := func() {
		m.Update(press(m, "p")())
		m.Update(m.refreshStatus())
	}
	m.Update(m.refreshStatus())
	deliver(m, run(m, "/model"))
	if view := ansi.Strip(m.View().Content); strings.Count(view, "for peers") != 1 || !strings.Contains(view, "p for peers") {
		t.Fatalf("no model is chosen for peers yet:\n%s", view)
	}
	press(m, "down")
	p()
	if b.status.PeerAccess != "a1" || b.status.PeerModel != "gpt-4o-mini" || len(b.set) != 0 ||
		m.notice != "peers are answered with gpt-4o-mini of Ollama local" || m.noticeErr {
		t.Fatalf("for peers %q of %q, set %v, notice %q", b.status.PeerModel, b.status.PeerAccess, b.set, m.notice)
	}
	// The model of that name of the other access is not the one.
	if view := ansi.Strip(m.View().Content); strings.Count(view, "for peers") != 2 || !strings.Contains(view, "▸ gpt-4o-mini  for peers") || !strings.Contains(view, "gpt-4o  in use") {
		t.Fatalf("the model for peers should be marked, once:\n%s", view)
	}
	press(m, "down")
	p()
	if b.status.PeerAccess != "b2" || m.notice != "peers are answered with gpt-4o-mini of OpenCode Go" {
		t.Fatalf("for peers of %q, notice %q", b.status.PeerAccess, m.notice)
	}
	p()
	if b.status.PeerAccess != "" || b.status.PeerModel != "" || m.notice != "peers are answered with the model of the node again" {
		t.Fatalf("for peers %q of %q, notice %q", b.status.PeerModel, b.status.PeerAccess, m.notice)
	}
	if view := ansi.Strip(m.View().Content); strings.Count(view, "for peers") != 1 {
		t.Fatalf("no model is chosen for peers any more:\n%s", view)
	}

	b.fail = map[string]error{"setpeermodel": errors.New("disk full")}
	m.Update(press(m, "p")())
	if m.notice != "disk full" || !m.noticeErr {
		t.Fatalf("a model that cannot be saved: %q", m.notice)
	}
	// p picks nothing while the models are on their way.
	m.modelBusy = "listing models"
	if press(m, "p") != nil {
		t.Fatal("p while the models are listed")
	}
}

// f on the model screen makes the model selected the last of the fallbacks
// of the node, which the list marks with their order, and f on a fallback
// makes it none any more.
func TestModelScreenTogglesFallbacks(t *testing.T) {
	m, b := newModel(t)
	b.status.Model = "gpt-4o"
	b.offers = []Models{local("gpt-4o", "gpt-4o-mini"), plan("gpt-4o-mini")}
	b.fallbacks = []Fallback{{Access: "b2", Model: "gpt-4o-mini"}}
	deliver(m, run(m, "/model"))
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, "fallback 1") != 1 || strings.Contains(view, "fallback 2") || !strings.Contains(view, "f fallback") {
		t.Fatalf("the fallback of the node should be marked:\n%s", view)
	}
	press(m, "down")
	deliver(m, press(m, "f"))
	if len(b.fallbacks) != 2 || b.fallbacks[1] != (Fallback{Access: "a1", Model: "gpt-4o-mini"}) || m.noticeErr ||
		m.notice != "gpt-4o-mini of Ollama local is fallback 2: it answers you when what is before it cannot for now" {
		t.Fatalf("fallbacks %+v, notice %q", b.fallbacks, m.notice)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "▸ gpt-4o-mini  fallback 2") || strings.Count(view, "fallback 1") != 1 {
		t.Fatalf("both fallbacks should be marked:\n%s", view)
	}
	press(m, "down")
	deliver(m, press(m, "f"))
	if len(b.fallbacks) != 1 || m.notice != "gpt-4o-mini of OpenCode Go is no fallback any more" {
		t.Fatalf("fallbacks %+v, notice %q", b.fallbacks, m.notice)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "gpt-4o-mini  fallback 1") || strings.Contains(view, "fallback 2") || !strings.Contains(view, "▸ gpt-4o-mini\n") && !strings.Contains(view, "▸ gpt-4o-mini ") {
		t.Fatalf("the one that is left is the first:\n%s", view)
	}

	b.fail = map[string]error{"togglefallback": errors.New("the node answers with this model")}
	deliver(m, press(m, "f"))
	if m.notice != "the node answers with this model" || !m.noticeErr || len(m.fallbacks) != 1 {
		t.Fatalf("a fallback that cannot be: %q, %+v", m.notice, m.fallbacks)
	}
	// Fallbacks that cannot be read are no models to show.
	b.fail = map[string]error{"fallbacks": errors.New("the configuration cannot be read")}
	press(m, "esc")
	deliver(m, run(m, "/model"))
	if m.modelErr == nil || len(m.choices) != 0 {
		t.Fatalf("models without their fallbacks: %v, %d", m.modelErr, len(m.choices))
	}
}
