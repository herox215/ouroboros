//go:build cgo

package gui

import (
	_ "embed"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// appIcon is the picture of ouroboros the window and the desktop show.
//
//go:embed icon.png
var appIconData []byte

var appIcon = fyne.NewStaticResource("ouroboros.png", appIconData)

// The palette of the window, taken from frontend/style.css: the same
// background, text, muted line and accent colours, light and dark.
type palette struct {
	background        color.NRGBA
	text              color.NRGBA
	surface           color.NRGBA
	field             color.NRGBA
	hover             color.NRGBA
	line              color.NRGBA
	lineStrong        color.NRGBA
	selected          color.NRGBA
	sessionHover      color.NRGBA
	error             color.NRGBA
	errorLine         color.NRGBA
	errorBackground   color.NRGBA
	warningLine       color.NRGBA
	warningBackground color.NRGBA
	toolBackground    color.NRGBA
	muted             color.NRGBA
	accent            color.NRGBA
}

func hex(value uint32) color.NRGBA {
	return color.NRGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 0xff}
}

func withAlpha(c color.NRGBA, alpha uint8) color.NRGBA {
	c.A = alpha
	return c
}

var lightPalette = palette{
	background:        hex(0xeae7e1),
	text:              hex(0x4d503b),
	surface:           hex(0xffffff),
	field:             hex(0xfdfcf9),
	hover:             hex(0xeef0e7),
	line:              hex(0xcccec0),
	lineStrong:        hex(0xc2c3b6),
	selected:          withAlpha(hex(0xdedfd3), 0x70),
	sessionHover:      withAlpha(hex(0xdedfd3), 0x40),
	error:             hex(0x873a29),
	errorLine:         hex(0xddbbb0),
	errorBackground:   hex(0xfbefe9),
	warningLine:       hex(0xe4dcc0),
	warningBackground: hex(0xfbf7ea),
	toolBackground:    hex(0xf0efe9),
	muted:             hex(0x828371),
	accent:            hex(0x53583b),
}

var darkPalette = palette{
	background:        hex(0x20221d),
	text:              hex(0xe1e4d4),
	surface:           hex(0x2b2e26),
	field:             hex(0x272a23),
	hover:             hex(0x34392c),
	line:              hex(0x505645),
	lineStrong:        hex(0x777f65),
	selected:          withAlpha(hex(0xc1cc99), 0x24),
	sessionHover:      withAlpha(hex(0xc1cc99), 0x10),
	error:             hex(0xf0ad96),
	errorLine:         hex(0x855447),
	errorBackground:   hex(0x3d2b25),
	warningLine:       hex(0x6e6440),
	warningBackground: hex(0x332f22),
	toolBackground:    hex(0x282b24),
	muted:             hex(0xa5a897),
	accent:            hex(0xc1cc99),
}

// The size names the window uses besides the standard ones, in pixels as the
// stylesheet writes them.
const (
	sizeTiny    fyne.ThemeSizeName = "ouroTiny"
	sizeSmall   fyne.ThemeSizeName = "ouroSmall"
	sizeMeta    fyne.ThemeSizeName = "ouroMeta"
	sizeBody    fyne.ThemeSizeName = "ouroBody"
	sizeTitle   fyne.ThemeSizeName = "ouroTitle"
	sizeSection fyne.ThemeSizeName = "ouroSection"
	sizeHeading fyne.ThemeSizeName = "ouroHeading"
	sizeIcon    fyne.ThemeSizeName = "ouroIcon"
)

// ouroTheme draws the window in the colours and the monospace face of the
// stylesheet. pref is system, light or dark, as the settings keep it.
type ouroTheme struct {
	pref string
}

// dark reports whether the window draws dark in the variant the desktop
// asks for: always with the dark choice, never with the light one, and as
// the desktop does with the system one, as the web view of the GUI does.
func (t *ouroTheme) dark(variant fyne.ThemeVariant) bool {
	switch t.pref {
	case "dark":
		return true
	case "light":
		return false
	}
	return variant == theme.VariantDark
}

func (t *ouroTheme) p(variant fyne.ThemeVariant) *palette {
	if t.dark(variant) {
		return &darkPalette
	}
	return &lightPalette
}

// pal is the palette the window draws in now, for what draws its own
// colours instead of asking the theme.
func pal() *palette {
	a := fyne.CurrentApp()
	if a == nil {
		return &lightPalette
	}
	t, ok := a.Settings().Theme().(*ouroTheme)
	if !ok {
		return &lightPalette
	}
	return t.p(a.Settings().ThemeVariant())
}

// Color is the colour of name in the variant the settings show.
func (t *ouroTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	p := t.p(variant)
	switch name {
	case theme.ColorNameBackground:
		return p.background
	case theme.ColorNameForeground:
		return p.text
	case theme.ColorNameDisabled:
		return p.muted
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		return p.accent
	case theme.ColorNameError:
		return p.error
	case theme.ColorNameInputBackground, theme.ColorNameOverlayBackground:
		return p.background
	case theme.ColorNameMenuBackground:
		return p.surface
	case theme.ColorNameFocus:
		return withAlpha(p.accent, 0x30)
	case theme.ColorNamePressed:
		return withAlpha(p.accent, 0x30)
	case theme.ColorNameScrollBar:
		return withAlpha(p.muted, 0x90)
	case theme.ColorNamePlaceHolder:
		return p.muted
	case theme.ColorNameInputBorder:
		return p.line
	case theme.ColorNameHeaderBackground:
		return p.surface
	case theme.ColorNameButton, theme.ColorNameDisabledButton:
		return p.surface
	case theme.ColorNameHover:
		return p.hover
	case theme.ColorNameSeparator:
		return p.line
	case theme.ColorNameShadow:
		return color.NRGBA{}
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnError:
		return p.background
	case theme.ColorNameSelection:
		return p.selected
	}
	return theme.DefaultTheme().Color(name, variant)
}

// Font is the monospace face of the window, as the stylesheet sets it.
func (t *ouroTheme) Font(style fyne.TextStyle) fyne.Resource {
	return monoFont(style)
}

// Icon is the standard icon of the size the window draws it.
func (t *ouroTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// Size is the text and padding size of the stylesheet.
func (t *ouroTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case sizeTiny:
		return 9
	case sizeSmall:
		return 10
	case sizeMeta:
		return 11
	case sizeBody:
		return 12
	case sizeTitle:
		return 14
	case sizeSection:
		return 16
	case sizeHeading:
		return 20
	case sizeIcon:
		return 23
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameSubHeadingText:
		return 14
	case theme.SizeNameHeadingText:
		return 16
	case theme.SizeNamePadding:
		return 4
	case theme.SizeNameInnerPadding:
		return 6
	case theme.SizeNameLineSpacing:
		return 0
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameInputBorder:
		// Fyne also uses the input border width for the text caret.
		return 1
	case theme.SizeNameInputRadius:
		return 0
	case theme.SizeNameButtonRadius:
		return 8
	case theme.SizeNameSelectionRadius:
		return 0
	}
	return theme.DefaultTheme().Size(name)
}

// useTheme applies the appearance preference to the window: the desktop's
// unless light or dark was chosen.
func (w *Window) useTheme(app fyne.App) {
	app.Settings().SetTheme(&ouroTheme{pref: themePreference(app)})
}

// themePreference is the appearance kept in the settings, system unless
// light or dark was chosen.
func themePreference(app fyne.App) string {
	pref := app.Preferences().StringWithFallback(themePreferenceKey, "system")
	switch pref {
	case "light", "dark":
		return pref
	}
	return "system"
}

const themePreferenceKey = "ouroboros.gui.theme"
