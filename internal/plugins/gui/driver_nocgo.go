//go:build !cgo || !production

package gui

import (
	"context"
	"errors"
)

func showWindow(context.Context, *profile, string) error {
	return errors.New("ouro gui needs a native Wails build: run make build with CGO enabled and the Wails system dependencies installed")
}
