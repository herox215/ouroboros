package tui

import (
	"image/color"
	"slices"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ouroboros/plugin"
)

// The UI wears the colors of the window of the GUI (see the style.css of
// internal/plugins/gui/frontend): an olive guiPalette, light or dark as the
// background of the terminal is. Once the terminal said which it is, the UI
// paints its background and its text in the colors of that palette, so that
// it looks as the window does, whatever theme the terminal has.

// guiPalette is one of the two sets of colors of the GUI.
type guiPalette struct {
	background, text, surface, field, hover, lineStrong, line, muted, accent color.Color
	selected                                                                 color.Color // the background of the session shown in the sidebar
	err, errLine, errBackground                                              color.Color
	warnLine, warnBackground                                                 color.Color
	toolBackground                                                           color.Color // the background of a block of code
	warn                                                                     color.Color // what waits for a decision; the GUI says it with its background only
}

var (
	guiDark = guiPalette{
		background: lipgloss.Color("#20221d"), text: lipgloss.Color("#e1e4d4"), surface: lipgloss.Color("#2b2e26"),
		field: lipgloss.Color("#272a23"), hover: lipgloss.Color("#34392c"), lineStrong: lipgloss.Color("#777f65"),
		line: lipgloss.Color("#505645"), muted: lipgloss.Color("#a5a897"), accent: lipgloss.Color("#c1cc99"),
		err: lipgloss.Color("#f0ad96"), errLine: lipgloss.Color("#855447"), errBackground: lipgloss.Color("#3d2b25"),
		warnLine: lipgloss.Color("#6e6440"), warnBackground: lipgloss.Color("#332f22"), toolBackground: lipgloss.Color("#282b24"),
		warn: lipgloss.Color("#d8c98f"),
	}
	guiLight = guiPalette{
		background: lipgloss.Color("#eae7e1"), text: lipgloss.Color("#4d503b"), surface: lipgloss.Color("#ffffff"),
		field: lipgloss.Color("#fdfcf9"), hover: lipgloss.Color("#eef0e7"), lineStrong: lipgloss.Color("#c2c3b6"),
		line: lipgloss.Color("#cccec0"), muted: lipgloss.Color("#828371"), accent: lipgloss.Color("#53583b"),
		err: lipgloss.Color("#873a29"), errLine: lipgloss.Color("#ddbbb0"), errBackground: lipgloss.Color("#fbefe9"),
		warnLine: lipgloss.Color("#e4dcc0"), warnBackground: lipgloss.Color("#fbf7ea"), toolBackground: lipgloss.Color("#f0efe9"),
		warn: lipgloss.Color("#8a6d1f"),
	}
)

func init() {
	// The GUI's selection is a translucent accent; the terminal has no
	// alpha, so it is mixed into the background once.
	guiDark.selected = blend(lipgloss.Color("#c1cc99"), guiDark.background, 0x24/255.0)
	guiLight.selected = blend(lipgloss.Color("#dedfd3"), guiLight.background, 0x70/255.0)
	applyPalette(false)
}

// ui is the palette the UI is drawn in now.
var ui = guiDark

// applyPalette makes the guiPalette for a light or a dark background the one
// everything is drawn in: the colors of the UI, and the styles made of them.
func applyPalette(light bool) {
	ui = guiDark
	if light {
		ui = guiLight
	}
	accent, muted, bad, warn = ui.accent, ui.muted, ui.err, ui.warn
	good, peer = ui.accent, ui.muted // the GUI has no green, and heads every speaker alike
	codeColor = ui.accent
	mutedStyle = lipgloss.NewStyle().Foreground(muted)
	errStyle = lipgloss.NewStyle().Foreground(bad)
	imageStyle = lipgloss.NewStyle().Foreground(accent).Underline(true)
	keyStyle = lipgloss.NewStyle().Foreground(accent)
	stringStyle = lipgloss.NewStyle().Foreground(ui.text)
	literalStyle = lipgloss.NewStyle().Foreground(warn)
	noteStyle = mutedStyle.Italic(true)
	currentStyle = lipgloss.NewStyle().Foreground(ui.background).Background(warn).Bold(true)
	blockStyle = lipgloss.NewStyle().Foreground(muted).Background(muted)
	roleColors = map[string]color.Color{
		"user":              accent,
		"assistant":         ui.text,
		"system":            ui.lineStrong,
		"tool":              ui.muted,
		"plugin diagnostic": bad,
	}
}

// mix is the color a of the share t over b, as a translucent a looks on b.
func blend(a, b color.Color, t float64) color.Color {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	at := func(x, y uint32) uint8 {
		return uint8((float64(x>>8)*t + float64(y>>8)*(1-t)) + 0.5)
	}
	return color.RGBA{at(ar, br), at(ag, bg), at(ab, bb), 0xff}
}

// faded is c as the GUI shows it with an opacity of t on the background.
func faded(c color.Color, t float64) color.Color { return blend(c, ui.background, t) }

// The owner chooses how the UI looks, as in the settings of the GUI: as the
// background of the terminal is, or light or dark whatever it is. The
// choice holds for every profile and the next time the UI opens, where the
// backend keeps it.

// appearances are how the UI can look, the one it starts with first.
var appearances = plugin.Appearances

// appearanceBackend keeps how the UI looks, see plugin.Owner.Appearance. A
// UI of a backend without it looks as the terminal is.
type appearanceBackend interface {
	Appearance() string
	SetAppearance(string) error
}

// applyLook draws the UI in the palette its appearance says: for "system"
// that of the background of the terminal.
func (m *Model) applyLook() {
	light := m.terminalLight
	switch m.appearance {
	case "light":
		light = true
	case "dark":
		light = false
	}
	m.light = light
	applyPalette(light)
	m.input.SetStyles(inputStyles(light))
	if m.onboard != nil {
		m.onboard.input.SetStyles(textinput.DefaultStyles(!light))
	}
	m.renderHistory(m.followsEnd()) // in the colors of the palette
}

// siblings are the UIs of all the profiles the terminal shows, this one
// among them: they look alike.
func (m *Model) siblings() []*Model {
	if m.among != nil {
		return m.among.models
	}
	return []*Model{m}
}

// setAppearance makes the UI of every profile look as a says, from now on
// and the next time it opens.
func (m *Model) setAppearance(a string) tea.Cmd {
	for _, o := range m.siblings() {
		o.appearance = a
		o.applyLook()
	}
	done := "appearance " + a
	b, ok := m.backend.(appearanceBackend)
	if !ok {
		m.setNotice(done, false)
		return nil
	}
	return func() tea.Msg { return actionMsg{done, b.SetAppearance(a)} }
}

// nextAppearance is what /appearance does: the UI looks as the next of the
// appearances says.
func (m *Model) nextAppearance() tea.Cmd {
	i := slices.Index(appearances, m.appearance)
	return m.setAppearance(appearances[(i+1)%len(appearances)])
}

// appearanceText is how the UI looks, as the settings say it: for "system"
// with what the terminal is.
func (m *Model) appearanceText() string {
	if m.appearance != "system" {
		return m.appearance
	}
	if m.light {
		return "system · light"
	}
	return "system · dark"
}
