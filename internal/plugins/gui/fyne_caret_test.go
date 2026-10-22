//go:build cgo

package gui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

func TestFyneEntryCaretVisible(t *testing.T) {
	for _, pref := range []string{"light", "dark"} {
		t.Run(pref, func(t *testing.T) {
			app := test.NewApp()
			defer app.Quit()
			app.Settings().SetTheme(&ouroTheme{pref: pref})
			for _, entry := range []fyne.Widget{newComposerEntry(func(string) {}), newBorderlessEntry(false), newBorderlessEntry(true)} {
				window := test.NewWindow(entry)
				window.Resize(fyne.NewSize(300, 100))
				assertBorderless := func() {
					t.Helper()
					border := test.WidgetRenderer(entry).Objects()[1].(*canvas.Rectangle)
					_, _, _, alpha := border.StrokeColor.RGBA()
					if alpha != 0 {
						t.Fatal("entry border is not transparent")
					}
				}
				assertBorderless()
				window.Canvas().Focus(entry.(fyne.Focusable))
				assertBorderless()
				test.Type(entry.(fyne.Focusable), "ab")
				var caret *canvas.Rectangle
				var find func(fyne.CanvasObject)
				find = func(object fyne.CanvasObject) {
					if rect, ok := object.(*canvas.Rectangle); ok && rect.Visible() && rect.Size().Width == 1 && rect.Size().Height > 1 {
						caret = rect
					}
					switch object := object.(type) {
					case *fyne.Container:
						for _, child := range object.Objects {
							find(child)
						}
					case fyne.Widget:
						for _, child := range test.WidgetRenderer(object).Objects() {
							find(child)
						}
					}
				}
				find(entry)
				if caret == nil {
					t.Fatal("focused entry has no visible, one-pixel-wide caret")
				}
				before := caret.Position()
				entry.(fyne.Focusable).TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeft})
				if caret.Position().X >= before.X {
					t.Fatal("caret did not move left with the insertion point")
				}
				entry.Refresh()
				assertBorderless()
				window.Canvas().Unfocus()
				assertBorderless()
				window.Close()
			}
		})
	}
}
