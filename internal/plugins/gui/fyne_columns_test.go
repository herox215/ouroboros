//go:build cgo

package gui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

func TestColumnsRevealOnlyContent(t *testing.T) {
	widgetApp(t, "light")
	ui := &fyneUI{sessionsHead: box(10, 10), contextDetails: container.NewVBox()}
	ui.sessionsHead.Hide()
	l := &columnsLayout{ui: ui}
	main, rail, scrim, sidebar := box(10, 10), box(10, 10), box(10, 10), box(10, 10)
	ui.columns = container.New(l, main, rail, scrim, sidebar)
	size := fyne.NewSize(1200, 700)
	ui.columns.Resize(size)
	if sidebar.Visible() || rail.Visible() || main.Size() != size {
		t.Fatal("empty panels occupy space")
	}
	ui.sessionsHead.Show()
	ui.contextDetails.Add(box(10, 10))
	ui.columns.Refresh()
	l.animation.Stop()
	l.frame(0.5)
	middle := main.Position().X
	if middle <= 0 || middle >= 276 || !sidebar.Visible() || !rail.Visible() {
		t.Fatalf("no intermediate layout: %v", main.Position())
	}
	if main.Size().Width != size.Width {
		t.Fatalf("the chat reflowed while the panels slid: %v", main.Size())
	}
	l.frame(1)
	if main.Position().X != 276 || main.Size().Width != 648 {
		t.Fatalf("content layout: %v %v", main.Position(), main.Size())
	}
	ui.sessionsHead.Hide()
	ui.contextDetails.RemoveAll()
	ui.columns.Refresh()
	l.animation.Stop()
	l.frame(0.5)
	if main.Position().X != middle {
		t.Fatal("closing jumped instead of interpolating")
	}
	l.frame(1)
	if sidebar.Visible() || rail.Visible() || main.Size() != size {
		t.Fatal("empty panels did not release their space")
	}
}
