package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"ouroboros/internal/speech"
)

// speechBackend is optional so other terminal backends need not support speech.
// The plugin UI embeds Owner, which provides these methods.
type speechBackend interface {
	SpeechSettings() (speech.Snapshot, error)
	SaveSpeech(speech.Config) error
	DownloadSpeech() error
	CancelSpeechDownload() error
}

type speechScreen struct {
	generation   uint64
	snapshot     speech.Snapshot
	draft        speech.Config
	loaded, busy bool
	failed       bool
}
type speechMsg struct {
	owner      *Model
	generation uint64
	snapshot   speech.Snapshot
	failed     bool
	operation  string
}
type speechTick struct {
	owner      *Model
	generation uint64
}

func (c *settingsScreen) speechOpen(m *Model) bool {
	return m.screen == screenSettings && c.opened && c.section() == speechSection
}

func (c *settingsScreen) closeSpeech() {
	c.speech.generation++
	c.speech.loaded, c.speech.busy = false, false
	c.speech.draft.APIKey = ""
	c.input.SetValue("")
}

func (c *settingsScreen) readSpeech(m *Model) tea.Cmd {
	b, ok := m.backend.(speechBackend)
	if !ok {
		return nil
	}
	generation := c.speech.generation
	return func() tea.Msg {
		s, err := b.SpeechSettings()
		s.Config.APIKey = ""
		return speechMsg{owner: m, generation: generation, snapshot: s, failed: err != nil}
	}
}

func (c *settingsScreen) speechLoaded(m *Model, msg speechMsg) tea.Cmd {
	if msg.owner != m || msg.generation != c.speech.generation || !c.speechOpen(m) {
		return nil
	}
	s := &c.speech
	s.busy, s.failed = false, msg.failed
	if !msg.failed {
		s.snapshot = msg.snapshot
		if !s.loaded || msg.operation == "Save" {
			s.draft = msg.snapshot.Config
			s.draft.APIKey = ""
		}
		s.loaded = true
	}
	if msg.operation != "" {
		if msg.failed {
			m.setNotice("Speech operation failed; check the settings and local prerequisites", true)
		} else {
			m.setNotice("Speech: "+msg.operation, false)
		}
	}
	generation := s.generation
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return speechTick{m, generation} })
}

func (c *settingsScreen) speechOperation(m *Model, operation string) tea.Cmd {
	b, ok := m.backend.(speechBackend)
	if !ok || c.speech.busy {
		return nil
	}
	c.speech.generation++ // invalidate outstanding refreshes before changing settings
	generation := c.speech.generation
	change := c.speech.draft
	c.speech.draft.APIKey = ""
	c.input.SetValue("")
	c.speech.busy = true
	return func() tea.Msg {
		var err error
		switch operation {
		case "Save":
			err = b.SaveSpeech(change)
		case "Download/setup":
			err = b.DownloadSpeech()
		case "Cancel":
			err = b.CancelSpeechDownload()
		}
		change.APIKey = ""
		s, readErr := b.SpeechSettings()
		s.Config.APIKey = ""
		return speechMsg{owner: m, generation: generation, snapshot: s, failed: err != nil || readErr != nil, operation: operation}
	}
}

func (c *settingsScreen) speechRows(m *Model) []row {
	if _, ok := m.backend.(speechBackend); !ok {
		return []row{{label: "Speech recognition", value: "unavailable"}}
	}
	s := &c.speech
	if !s.loaded {
		value := "Loading…"
		if s.failed {
			value = "Could not read speech settings; retry by reopening this category"
		}
		return []row{{label: "Speech recognition", value: value}}
	}
	d := &s.draft
	editRow := func(label, value string, secret bool, assign func(string)) row {
		return row{label: label, value: value, actions: []action{{"Edit", func(m *Model) tea.Cmd {
			initial := value
			if secret {
				initial = ""
			}
			return c.gatewayEdit(m, label, initial, secret, func(v string) tea.Cmd { assign(v); return nil })
		}}}}
	}
	provider := row{label: "Provider", value: d.Provider}
	for _, p := range []string{"disabled", "local", "remote"} {
		provider.actions = append(provider.actions, action{p, func(*Model) tea.Cmd {
			d.Provider = p
			if p != "remote" && !slices.ContainsFunc(s.snapshot.Models, func(v speech.Model) bool { return v.ID == d.Model }) {
				d.Model = "large-v3-turbo-q5_0"
			}
			return nil
		}})
	}
	model := row{label: "Local model", value: d.Model}
	for _, item := range s.snapshot.Models {
		model.actions = append(model.actions, action{item.ID, func(*Model) tea.Cmd { d.Model = item.ID; return nil }})
	}
	if d.Provider == "remote" {
		model = editRow("Remote model", d.Model, false, func(v string) { d.Model = v })
	}
	key := "not set (blank keeps existing key)"
	if s.snapshot.HasKey || d.APIKey != "" {
		key = "•••••••• (blank keeps existing key)"
	}
	status := "unknown"
	switch s.snapshot.Status {
	case "disabled", "remote", "downloading", "error", "missing_ffmpeg", "missing_runtime", "missing_model", "ready":
		status = s.snapshot.Status
	}
	out := []row{provider, model,
		editRow("Endpoint", d.Endpoint, false, func(v string) { d.Endpoint = v }),
		editRow("API key", key, true, func(v string) { d.APIKey = v }),
		editRow("Language", d.Language, false, func(v string) { d.Language = v }),
		{label: "Status", value: status},
		{label: "Readiness", value: fmt.Sprintf("Runtime: %t · Model: %t", s.snapshot.RuntimeReady, s.snapshot.ModelReady)},
		speechProgressRow(s.snapshot.Download),
	}
	for _, op := range []string{"Save", "Download/setup", "Cancel"} {
		out = append(out, row{label: op, actions: []action{{op, func(m *Model) tea.Cmd { return c.speechOperation(m, op) }}}})
	}
	out[len(out)-2].note = "Downloads the saved local model and prepares whisper.cpp. Save settings first. Saving alone never downloads."
	if s.failed {
		out = append(out, row{label: "Speech operation failed", value: "Check settings and local prerequisites; details hidden for privacy."})
	}
	if s.busy {
		for i := range out {
			out[i].actions = nil
		}
		out = append(out, row{label: "Working…"})
	}
	return out
}

func speechProgress(p speech.Progress) string {
	// Only known phases are displayed: backend errors and arbitrary status text
	// can contain credentials or paths and must never reach the terminal.
	phase := "idle"
	switch p.Phase {
	case "starting", "runtime_download", "runtime_build", "error", "checking", "downloading", "runtime", "model", "building", "extracting", "complete", "cancelled", "failed", "source", "configure", "build", "download":
		phase = p.Phase
	default:
		if p.Active {
			phase = "preparing"
		}
	}
	if p.Error != "" {
		phase = "failed (details hidden)"
	}
	received, total := max(p.Received, 0), max(p.Total, 0)
	if total == 0 {
		return fmt.Sprintf("%s · %d bytes (total unknown)", phase, received)
	}
	percent := min(100, float64(received)/float64(total)*100)
	filled := int(percent / 5)
	return fmt.Sprintf("%s [%s%s] %.0f%% · %d / %d bytes", phase, strings.Repeat("=", filled), strings.Repeat("-", 20-filled), percent, received, total)
}

// Give phases and byte counts their own lines so the row label does not
// consume the space needed by a realistic model size in an 80-column terminal.
func speechProgressRow(p speech.Progress) row {
	text := speechProgress(p)
	split := strings.Index(text, " [")
	if split < 0 {
		split = strings.Index(text, " · ")
	}
	if split < 0 {
		return row{label: "Download progress", value: text}
	}
	return row{label: "Download progress", value: text[:split], details: []detail{{value: strings.TrimSpace(strings.TrimPrefix(text[split:], " · "))}}}
}
