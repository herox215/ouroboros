package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ouroboros/plugin"
)

// localGUI checks only an explicit GUI launch from a source checkout. Go's
// build cache checks the actual inputs, including uncommitted files; neither a clean git status nor timestamps prove a binary is current.
func localGUI(ctx context.Context, args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "--profile=") {
		args = args[1:]
	} else if len(args) > 1 && args[0] == "--profile" {
		args = args[2:]
	}
	if !(len(args) == 1 && args[0] == "gui") &&
		!(len(args) == 2 && args[0] == "desktop" && args[1] == "open") {
		return nil
	}
	exe, err := executable()
	if err != nil {
		return err
	}
	running := exe
	if runtime.GOOS == "linux" {
		// This stays the running image even after another launch replaces exe.
		running = "/proc/self/exe"
	}
	return localGUIBuild(ctx, exe, running, os.Stderr)
}

// executable finds the running binary; tests replace it.
var executable = os.Executable

func localGUIBuild(ctx context.Context, exe, running string, out io.Writer) error {
	root := filepath.Dir(exe)
	// Installed copies must not build some unrelated working directory, or
	// implicitly update themselves from the checkout recorded at link time.
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.HasPrefix(string(mod), "module ouroboros\n") {
		return nil
	}
	for _, name := range []string{"Makefile", "cmd/ouro/main.go"} {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || info.IsDir() {
			return nil
		}
	}
	// Capture the running image before the build, not the pathname another
	// launch may replace while we are building.
	before, err := binaryHash(running)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Checking the local GUI build…")
	// Stage on the same filesystem: a failed build never replaces the binary,
	// and a successful replacement is atomic even while the old binary runs.
	stage, err := os.MkdirTemp(root, ".ouro-gui-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	built := filepath.Join(stage, "ouro")
	cmd := exec.CommandContext(ctx, "make", "build", "BIN="+built)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local GUI build failed; not starting the old GUI: %w", err)
	}
	after, err := binaryHash(built)
	if err != nil {
		return err
	}
	if before == after {
		return nil
	}
	if err := os.Rename(built, exe); err != nil {
		return err
	}
	return &plugin.Restarting{Exe: exe, Env: os.Environ()}
}

func binaryHash(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
