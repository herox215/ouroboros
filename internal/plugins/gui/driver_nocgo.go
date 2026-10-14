//go:build !cgo

package gui

import (
	"context"
	"errors"
)

func showWindow(context.Context, page) error {
	return errors.New("ouro gui needs a build with CGO enabled and the native Fyne dependencies (OpenGL and a desktop window system)")
}
