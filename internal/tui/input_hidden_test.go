package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The box of the input is hidden until text typed on the chat shows it, and an
// empty input hides again inputIdleTime after the last key, on a tick of the
// spinner. It slides in and out, and the chat gives it its rows and takes
// them back as it goes.
func TestInputHidden(t *testing.T) {
	m, _ := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	m.inputShown = false
	m.layout()
	tall := m.chatHeight()
	if strings.Contains(ansi.Strip(m.chatView()), "Keep on dreaming") || m.history.Height() != tall-3 {
		t.Fatalf("the input should be hidden first, and the chat %d rows high:\n%s", tall, ansi.Strip(m.chatView()))
	}
	if hint := footer(m); hint != "type to write" {
		t.Fatalf("the footer should say that typing brings the input: %q", hint)
	}
	tick := func() { m.Update(spinner.TickMsg{ID: m.spinner.ID()}) }
	fills := func() {
		t.Helper()
		if rows := strings.Count(m.View().Content, "\n") + 1; rows != m.height {
			t.Fatalf("the view is %d rows high, not %d:\n%s", rows, m.height, view(m))
		}
	}
	fills()
	// fade runs the frames of the box on its way, from the frame cmd asks
	// for: the chat is a row higher or lower with each, or as high as before,
	// and the view fills the window in every one.
	fade := func(cmd tea.Cmd, in bool) {
		t.Helper()
		if cmd == nil || !m.inputFading || !brings(cmd) {
			t.Fatal("the box of the input should be on its way")
		}
		n := 0
		for was := m.chatHeight(); m.inputFading; n++ {
			if _, next := m.Update(inputFadeMsg{}); m.inputFading && (next == nil || n == 0 && !brings(next)) {
				t.Fatalf("frame %d should ask for the next", n)
			}
			step := was - m.chatHeight()
			if !in {
				step = -step
			}
			if step < 0 || step > 2 {
				t.Fatalf("frame %d: the chat went from %d to %d rows", n, was, m.chatHeight())
			}
			if m.inputLevel > 0 && m.inputLevel < inputFrames && strings.Contains(m.chatColumn(), "\x1b[2m") == (m.inputLevel*2 < inputFrames) {
				t.Fatalf("frame %d: the box should be gray first, then dim", n)
			}
			was = m.chatHeight()
			fills()
		}
		if n != inputFrames {
			t.Fatalf("the box took %d frames, not %d", n, inputFrames)
		}
	}

	// Shortcuts leave it hidden.
	for _, key := range []string{"pgup", "pgdown", "up", "down", "esc", "enter", "tab", "ctrl+d"} {
		if press(m, key); m.inputVisible() {
			t.Fatalf("the shortcut %s should not show the input", key)
		}
	}
	if m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}); m.inputVisible() || m.screen != screenKeys {
		t.Fatal("ctrl+k should open the keys, and not show the input")
	}
	if press(m, "esc"); m.inputVisible() || m.screen != screenChat {
		t.Fatal("esc should close the keys, and not show the input")
	}

	fade(press(m, "x"), true)
	if !strings.Contains(ansi.Strip(m.chatView()), "│ x") {
		t.Fatalf("a key on the chat should show the input:\n%s", ansi.Strip(m.chatView()))
	}
	if m.chatHeight() != tall-inputHeight-2 || m.history.Height() != tall-inputHeight-2-3 {
		t.Fatalf("the chat should make room for the input: %d rows", m.chatHeight())
	}
	fills()
	if hint := footer(m); hint == "type to write" {
		t.Fatal("with the input shown the footer need not say it")
	}
	at = at.Add(time.Minute)
	if tick(); !m.inputVisible() {
		t.Fatal("an input with text stays")
	}

	press(m, "backspace")
	at = at.Add(inputIdleTime - time.Millisecond)
	if tick(); !m.inputVisible() {
		t.Fatal("the empty input should stay until it is idle")
	}
	at = at.Add(time.Millisecond)
	_, hide := m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if m.inputVisible() {
		t.Fatal("the empty input should hide when idle")
	}
	if fade(hide, false); m.chatHeight() != tall || m.history.Height() != tall-3 {
		t.Fatalf("the empty input should hide when idle, and the chat take its room: %d rows", m.chatHeight())
	}
	fills()

	m.input.SetValue("pasted")
	if !m.inputVisible() {
		t.Fatal("an input with text is shown")
	}
	m.input.Reset()

	// Text typed while the box goes out turns it around, without a frame of
	// its own on the way.
	fade(press(m, "z"), true)
	press(m, "backspace")
	at = at.Add(time.Minute)
	tick()
	m.Update(inputFadeMsg{})
	m.Update(inputFadeMsg{})
	if press(m, "y"); !m.inputFading || m.inputLevel != inputFrames-2 {
		t.Fatalf("the box should turn around at level %d", m.inputLevel)
	}
	for m.inputFading {
		m.Update(inputFadeMsg{})
	}
	if m.inputLevel != inputFrames || m.chatHeight() != tall-inputHeight-2 {
		t.Fatalf("the box should be back in: level %d", m.inputLevel)
	}
	m.input.Reset()

	m.Update(eventMsg{id: m.chat.contextID, event: Event{Kind: plugin.SessionAsked, Text: "which?", Options: []string{"a", "b"}}})
	if !m.inputVisible() || m.inputFading || m.chatHeight() != tall-inputHeight-2 {
		t.Fatal("a question of the agent should show the input at once")
	}
}

// brings reports whether cmd, or one of a batch of them, brings a frame of
// the box of the input.
func brings(cmd tea.Cmd) bool {
	switch msg := cmd().(type) {
	case inputFadeMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if c != nil && brings(c) {
				return true
			}
		}
	}
	return false
}
