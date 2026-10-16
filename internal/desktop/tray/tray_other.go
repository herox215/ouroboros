//go:build !linux

// Package tray exposes Ouroboros as a StatusNotifierItem on Linux.
package tray

import (
	"context"
	"errors"
	"ouroboros/internal/desktop"
)

// Run is unsupported outside Linux; it never calls either callback.
func Run(ctx context.Context, sample func(context.Context) desktop.Status, open func()) error {
	return errors.New("tray: StatusNotifierItem is supported only on Linux; use the platform desktop launcher instead")
}
