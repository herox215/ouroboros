//go:build darwin && cgo

// Package tray exposes Ouroboros as a native desktop tray icon.
package tray

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void ouroTrayRun(void);
void ouroTrayUpdate(const char *json);
void ouroTrayStop(void);
*/
import "C"

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
	"unsafe"

	"ouroboros/internal/desktop"
)

const helperArgument = "--ouro-internal-macos-tray"

// AppKit must own the initial OS thread. A private child keeps its event loop
// separate from the serving node and from the GUI's own AppKit application.
func init() {
	if len(os.Args) != 2 || os.Args[1] != helperArgument {
		return
	}
	runtime.LockOSThread()
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		for scanner.Scan() {
			text := C.CString(scanner.Text())
			C.ouroTrayUpdate(text)
			C.free(unsafe.Pointer(text))
		}
		C.ouroTrayStop()
	}()
	C.ouroTrayRun()
	os.Exit(0)
}

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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, exe, helperArgument)
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
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if scanner.Text() == "open" {
				open()
			}
		}
		done <- cmd.Wait()
	}()
	encoder := json.NewEncoder(input)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err = encoder.Encode(nativeModel(sample(ctx))); err != nil {
			cancel()
			<-done
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tray: update macOS helper: %w", err)
		}
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case err = <-done:
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				err = errors.New("helper exited")
			}
			return fmt.Errorf("tray: macOS helper: %w", err)
		case <-ticker.C:
		}
	}
}
