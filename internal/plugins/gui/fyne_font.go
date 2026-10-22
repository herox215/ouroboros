//go:build cgo

package gui

import (
	"embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// The four faces of the monospace font of the window, embedded so that it
// looks the same where the desktop picks another one.
//
//go:embed fonts/IBMPlexMono-Regular.ttf fonts/IBMPlexMono-Bold.ttf fonts/IBMPlexMono-Italic.ttf fonts/IBMPlexMono-BoldItalic.ttf
var monoFontFiles embed.FS

var (
	monoRegular    = loadMonoFont("fonts/IBMPlexMono-Regular.ttf")
	monoBold       = loadMonoFont("fonts/IBMPlexMono-Bold.ttf")
	monoItalic     = loadMonoFont("fonts/IBMPlexMono-Italic.ttf")
	monoBoldItalic = loadMonoFont("fonts/IBMPlexMono-BoldItalic.ttf")
)

func loadMonoFont(name string) fyne.Resource {
	data, err := monoFontFiles.ReadFile(name)
	if err != nil {
		return theme.DefaultTextMonospaceFont()
	}
	return fyne.NewStaticResource(name, data)
}

// monoFont is the face of a text style.
func monoFont(style fyne.TextStyle) fyne.Resource {
	switch {
	case style.Bold && style.Italic:
		return monoBoldItalic
	case style.Bold:
		return monoBold
	case style.Italic:
		return monoItalic
	}
	return monoRegular
}
