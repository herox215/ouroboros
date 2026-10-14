//go:build cgo

package gui

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

func showWindow(ctx context.Context, page page) error {
	a := app.NewWithID("io.ouroboros.gui")
	w := a.NewWindow("Ouroboros · Demo")
	w.Resize(fyne.NewSize(800, 520))
	w.SetContent(content(page))
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			fyne.Do(w.Close)
		case <-done:
		}
	}()
	w.ShowAndRun()
	return ctx.Err()
}
