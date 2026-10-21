package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"ouroboros/internal/speech"
)

type fakeSpeechBackend struct {
	Backend
	snapshot                  speech.Snapshot
	saved                     speech.Config
	saves, downloads, cancels int
	err                       error
}

func (b *fakeSpeechBackend) SpeechSettings() (speech.Snapshot, error) { return b.snapshot, b.err }
func (b *fakeSpeechBackend) SaveSpeech(c speech.Config) error {
	b.saves++
	b.saved = c
	if b.err == nil {
		b.snapshot.Config = c
		b.snapshot.Config.APIKey = ""
		b.snapshot.HasKey = c.APIKey != "" || b.snapshot.HasKey
	}
	return b.err
}
func (b *fakeSpeechBackend) DownloadSpeech() error {
	b.downloads++
	b.snapshot.Download = speech.Progress{Active: true, Phase: "runtime_download", Received: 50, Total: 100}
	return b.err
}
func (b *fakeSpeechBackend) CancelSpeechDownload() error {
	b.cancels++
	b.snapshot.Download = speech.Progress{Phase: "cancelled"}
	return b.err
}
func openSpeech(t *testing.T) (*Model, *fakeSpeechBackend) {
	t.Helper()
	m, _ := openSettings(t, someSettings())
	b := &fakeSpeechBackend{Backend: m.backend, snapshot: speech.Snapshot{
		Config: speech.Config{Provider: "disabled", Model: "large-v3-turbo-q5_0", Endpoint: "https://example.test/v1", Language: "auto"},
		Models: []speech.Model{{ID: "large-v3-turbo-q5_0"}, {ID: "small"}},
	}}
	m.backend = b
	m.config.category = slices.Index(sections, speechSection)
	cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("opening category did not read speech settings")
	}
	m.Update(cmd()) // do not run the recurring refresh timer
	return m, b
}
func speechAction(t *testing.T, m *Model, label, action string) tea.Cmd {
	t.Helper()
	pick(t, m, speechSection, label, action)
	return press(m, "enter")
}
func TestSpeechSettingsSaveAndSetup(t *testing.T) {
	m, b := openSpeech(t)
	if !strings.Contains(dialogView(m), "disabled") || b.downloads != 0 {
		t.Fatal("speech defaults/open")
	}
	speechAction(t, m, "Provider", "local")
	speechAction(t, m, "Local model", "small")
	speechAction(t, m, "API key", "Edit")
	m.config.input.SetValue("private-key-value")
	if strings.Contains(dialogView(m), "private-key-value") {
		t.Fatal("key visible while editing")
	}
	press(m, "enter")
	if m.config.input.Value() != "" || strings.Contains(dialogView(m), "private-key-value") {
		t.Fatal("secret retained in input or shown")
	}
	cmd := speechAction(t, m, "Save", "Save")
	if m.config.speech.draft.APIKey != "" {
		t.Fatal("draft key not cleared on save")
	}
	m.Update(cmd())
	if b.saves != 1 || b.downloads != 0 || b.saved.Provider != "local" || b.saved.Model != "small" || b.saved.APIKey != "private-key-value" {
		t.Fatalf("save: %+v downloads %d", b.saved, b.downloads)
	}
	cmd = speechAction(t, m, "Download/setup", "Download/setup")
	m.Update(cmd())
	view := dialogView(m)
	if b.downloads != 1 || !strings.Contains(view, "50%") || !strings.Contains(view, "50 / 100 bytes") || !strings.Contains(view, "runtime_download") {
		t.Fatalf("download progress: %s", view)
	}
	cmd = speechAction(t, m, "Cancel", "Cancel")
	m.Update(cmd())
	if b.cancels != 1 || !strings.Contains(dialogView(m), "cancelled") {
		t.Fatal("cancel result not shown")
	}
}
func TestSpeechProviderTransitionsAndEdits(t *testing.T) {
	m, b := openSpeech(t)
	speechAction(t, m, "Provider", "remote")
	for _, field := range []struct{ label, value string }{{"Remote model", "custom-remote"}, {"Endpoint", "https://speech.example/v1"}, {"Language", "en"}} {
		speechAction(t, m, field.label, "Edit")
		m.config.input.SetValue(field.value)
		press(m, "enter")
	}
	m.Update(speechAction(t, m, "Save", "Save")())
	if b.saved.Model != "custom-remote" || b.saved.Language != "en" || b.saved.Endpoint != "https://speech.example/v1" {
		t.Fatalf("remote edits not saved: %+v", b.saved)
	}
	speechAction(t, m, "Provider", "local")
	if m.config.speech.draft.Model != "large-v3-turbo-q5_0" {
		t.Fatal("remote model did not reset to local whitelist")
	}
	for _, r := range m.config.speechRows(m) {
		if r.label == "Local model" {
			if len(r.actions) != 2 {
				t.Fatal("local model choices not from catalog")
			}
			for _, a := range r.actions {
				if a.name == "Edit" {
					t.Fatal("local model accepts arbitrary input")
				}
			}
		}
	}
	speechAction(t, m, "API key", "Edit")
	m.config.input.SetValue("discard-me")
	press(m, "esc")
	if m.config.input.Value() != "" || m.config.speech.draft.APIKey != "" {
		t.Fatal("cancelled key edit retained")
	}
}
func TestSpeechRefreshAndStaleResults(t *testing.T) {
	m, b := openSpeech(t)
	generation := m.config.speech.generation
	b.snapshot.Download = speech.Progress{Active: true, Phase: "model", Received: 75, Total: 100}
	_, cmd := m.Update(speechTick{m, generation})
	if cmd == nil {
		t.Fatal("refresh tick did not read snapshot")
	}
	stale := cmd()
	m.Update(stale)
	if !strings.Contains(dialogView(m), "75%") {
		t.Fatal("refresh result not visible")
	}
	// A pending read cannot roll back a newer save/download operation.
	op := speechAction(t, m, "Download/setup", "Download/setup")
	m.Update(stale)
	if !m.config.speech.busy {
		t.Fatal("old refresh cleared operation state")
	}
	m.Update(op())
	other, _ := newModel(t)
	m.Update(speechMsg{owner: other, generation: m.config.speech.generation, snapshot: speech.Snapshot{Config: speech.Config{Provider: "remote"}}})
	if m.config.speech.snapshot.Config.Provider == "remote" {
		t.Fatal("another profile's result applied")
	}
	pending := m.config.readSpeech(m)()
	press(m, "esc")
	m.Update(pending)
	_, cmd = m.Update(speechTick{m, generation})
	if cmd != nil || m.config.speech.loaded {
		t.Fatal("closed category accepted stale refresh")
	}
}
func TestSpeechErrorsAndOptionalBackend(t *testing.T) {
	m, b := openSpeech(t)
	b.err = errors.New("sensitive endpoint private-key-value")
	m.Update(speechAction(t, m, "Save", "Save")())
	if strings.Contains(dialogView(m), "private-key-value") || strings.Contains(m.notice, "private-key-value") {
		t.Fatal("raw backend error displayed")
	}
	if !m.config.speech.failed {
		t.Fatal("failure not reported")
	}
	if got := speechProgress(speech.Progress{Active: true, Phase: "secret-token", Error: "secret-token", Received: 7}); strings.Contains(got, "secret-token") || !strings.Contains(got, "7 bytes") {
		t.Fatal(got)
	}
	plain, _ := openSettings(t, someSettings())
	pick(t, plain, speechSection, "Speech recognition", "")
	if !strings.Contains(dialogView(plain), "unavailable") {
		t.Fatal("optional backend unsupported state")
	}
}

func TestSpeechProgressFitsTerminal(t *testing.T) {
	m, b := openSpeech(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	b.snapshot.Status = "missing_ffmpeg"
	b.snapshot.Download = speech.Progress{Active: true, Phase: "runtime_download", Received: 287020597, Total: 574041195}
	m.Update(m.config.readSpeech(m)())
	pick(t, m, speechSection, "Download progress", "")
	view := dialogView(m)
	for _, want := range []string{"runtime_download", "50%", "287020597 / 574041195 bytes", "[=========-----------]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in progress:\n%s", want, view)
		}
	}
}
