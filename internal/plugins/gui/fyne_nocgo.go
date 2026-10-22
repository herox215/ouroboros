//go:build !cgo

package gui

import (
	"context"
	"errors"
)

// runFyne is the command without cgo, where the window cannot be drawn.
func (p *Profile) runFyne(context.Context, []string) error {
	return errors.New("the Fyne window needs cgo: build ouro with CGO_ENABLED=1")
}
