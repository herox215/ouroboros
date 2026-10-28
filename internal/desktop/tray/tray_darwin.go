//go:build darwin && cgo

// Package tray exposes Ouroboros as a native desktop tray icon.
package tray

/*
#cgo LDFLAGS: -framework Cocoa
#include "tray_darwin.h"
*/
import "C"

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"ouroboros/internal/desktop"
)

// The helper itself lives in tray_darwin.m: AppKit must own the initial OS
// thread, so it starts before Go does. A private child keeps its event loop
// separate from the serving node and from the GUI's own AppKit application.
const helperArgument = C.OURO_TRAY_HELPER_ARGUMENT

// helperCommand starts this binary again as the helper; tests replace it.
var helperCommand = func(ctx context.Context) (*exec.Cmd, error) {
	exe, err := os.Executable()
	return exec.CommandContext(ctx, exe, helperArgument), err
}

// interval is how often the helper gets a fresh status; tests shorten it.
var interval = 2 * time.Second

type nativeStatus struct {
	Label     string
	Running   int
	Sessions  []string
	Attention bool
}

func nativeModel(s desktop.Status) nativeStatus {
	return nativeStatus{s.Label(), s.Running, s.Sessions, s.State == "waiting" || s.State == "error"}
}

// Run owns a native menu-bar helper until cancellation. Closing stdin also
// removes the icon when the parent exits unexpectedly. No node is started.
func Run(ctx context.Context, sample func(context.Context) desktop.Status, open func()) error {
	if sample == nil || open == nil {
		return errors.New("tray: sample and open callbacks are required")
	}
	if ctx.Err() != nil {
		return nil
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := helperCommand(childCtx)
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("tray: start macOS helper: %w", err)
	}
	var exited error
	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if scanner.Text() == "open" {
				open()
			}
		}
		exited = cmd.Wait()
		close(done)
	}()
	encoder := json.NewEncoder(input)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err = encoder.Encode(nativeModel(sample(ctx))); err != nil {
			err = fmt.Errorf("tray: update macOS helper: %w", err)
			break
		}
		select {
		case <-ctx.Done():
		case <-done:
			err = fmt.Errorf("tray: macOS helper: %w", cmp.Or(exited, errors.New("helper exited")))
		case <-ticker.C:
			continue
		}
		break
	}
	cancel()
	<-done
	if ctx.Err() != nil {
		return nil
	}
	return err
}
