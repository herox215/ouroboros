//go:build !cgo

package gui

import (
	"context"
	"errors"
)

// showWindow is the window without cgo, where it cannot be drawn.
func showWindow(context.Context, page) error {
	return errors.New("the window needs cgo: build ouro with CGO_ENABLED=1")
}
