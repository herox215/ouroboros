package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// lookBackend is a fakeBackend that keeps how the UI looks, as the owner of
// a node does.
type lookBackend struct {
	*fakeBackend
	appearance string
	err        error
}

func (b *lookBackend) Appearance() string { return b.appearance }
func (b *lookBackend) SetAppearance(a string) error {
	if b.err == nil {
		b.appearance = a
	}
	return b.err
}

// The UI looks as the terminal is, light or dark whatever it is, as the
// owner chose: in /settings, or with /appearance, which goes on to the next
// of them. The choice is kept.
func TestAppearance(t *testing.T) {
	b := &lookBackend{fakeBackend: newBackend(t), appearance: "light"}
	m := New(b, nil, nil)
	t.Cleanup(m.quit)
	t.Cleanup(func() { applyPalette(false) })
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splash.show = false
	m.Update(m.refreshStatus())
	if m.appearance != "light" || !m.light || ui.background != guiLight.background {
		t.Fatalf("a UI kept light starts as %q, light %v", m.appearance, m.light)
	}
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("0")})
	if !m.light || m.terminalLight || !m.backgroundKnown {
		t.Fatal("a light UI is light on a dark terminal")
	}
	if v := m.View(); v.BackgroundColor != guiLight.background {
		t.Fatalf("the terminal is painted %v", v.BackgroundColor)
	}
	cmd := run(m, "/appearance")
	if m.appearance != "dark" || m.light || cmd == nil {
		t.Fatalf("/appearance after light: %q", m.appearance)
	}
	if m.Update(cmd()); b.appearance != "dark" || m.notice != "appearance dark" {
		t.Fatalf("kept %q, notice %q", b.appearance, m.notice)
	}
	m.Update(run(m, "/appearance")())
	if m.appearance != "system" || m.light || m.appearanceText() != "system · dark" {
		t.Fatalf("/appearance after dark: %q, %q", m.appearance, m.appearanceText())
	}
	m.terminalLight = true
	m.applyLook()
	if !m.light || m.appearanceText() != "system · light" {
		t.Fatal("system follows the terminal")
	}

	// The settings show it among those of the node, and set it.
	b.settings = Settings{Name: "alpha"}
	deliver(m, run(m, "/settings"))
	press(m, "enter")
	if !strings.Contains(ansi.Strip(m.View().Content), "Appearance") {
		t.Fatalf("the settings do not show it:\n%s", ansi.Strip(m.View().Content))
	}
	rows := m.config.shown(m)
	appearance := rows[len(rows)-1]
	if appearance.label != "Appearance" || appearance.value != "system · light" || len(appearance.actions) != 4 {
		t.Fatalf("the row %+v", appearance)
	}
	b.err = errors.New("read-only")
	if m.Update(appearance.actions[2].run(m)()); m.appearance != "dark" || m.appearanceText() != "dark" || m.notice != "read-only" {
		t.Fatalf("dark: %q, notice %q", m.appearance, m.notice)
	}
}

// A UI of a backend that keeps no appearance follows the terminal, and is
// told of what it was set to.
func TestAppearanceWithoutAKeeper(t *testing.T) {
	m, _ := newModel(t)
	if m.appearance != "system" {
		t.Fatalf("starts as %q", m.appearance)
	}
	if cmd := run(m, "/appearance"); cmd != nil || m.appearance != "light" || !m.light || m.notice != "appearance light" {
		t.Fatalf("light: %q, notice %q", m.appearance, m.notice)
	}
}

// The profiles of one terminal look alike: what one is set to, all are,
// and what one learns of the terminal, all do.
func TestProfilesLookAlike(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	root.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if !terra.light || !luna.light || !luna.backgroundKnown {
		t.Fatal("a profile does not know the terminal")
	}
	terra.setAppearance("dark")
	if luna.appearance != "dark" || luna.light {
		t.Fatalf("the other profile is %q", luna.appearance)
	}
}
