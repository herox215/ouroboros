package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ouroboros/plugin"
)

// checkout makes a source checkout with the binary ouro in it, "old", and a
// make on PATH whose build runs body, with $dest the path to build to.
func checkout(t *testing.T, body string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	put := func(name, text string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	put("go.mod", "module ouroboros\n", 0644)
	put("cmd/ouro/main.go", "package main", 0644)
	put("Makefile", "", 0644)
	put("ouro", "old", 0755)
	// A real subprocess checks the command, working directory and BIN path.
	put("tools/make", "#!/bin/sh\n[ \"$1\" = build ] || exit 2\ndest=\"${2#BIN=}\"\n"+body+"\n", 0755)
	t.Setenv("PATH", filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root
}

// stale is a build whose binary differs from the old one.
const stale = "printf new > \"$dest\"; chmod 755 \"$dest\""

func TestLocalGUIBuild(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		restart, fail    bool
	}{
		{name: "current", body: "cp ouro \"$dest\"", want: "old"},
		{name: "stale", body: stale, want: "new", restart: true},
		{name: "concurrent replacement", body: "printf new > ouro; printf new > \"$dest\"; chmod 755 \"$dest\"", want: "new", restart: true},
		{name: "failure", body: "printf broken > \"$dest\"; exit 1", want: "old", fail: true},
		{name: "missing output", body: "true", want: "old", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := checkout(t, tc.body)
			exe := filepath.Join(root, "ouro")
			var out bytes.Buffer
			err := localGUIBuild(context.Background(), exe, exe, &out)
			r, restarting := errors.AsType[*plugin.Restarting](err)
			if restarting != tc.restart || (!restarting && (err != nil) != tc.fail) {
				t.Fatalf("error = %v, output = %s", err, &out)
			}
			if restarting && (r.Exe != exe || !slices.Equal(r.Env, os.Environ())) {
				t.Fatalf("restart = %+v", r)
			}
			got, err := os.ReadFile(exe)
			if err != nil || string(got) != tc.want {
				t.Fatalf("binary = %q, %v", got, err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(root, ".ouro-gui-*"))
			if len(leftovers) != 0 {
				t.Fatalf("staging directories left: %v", leftovers)
			}
			if !strings.Contains(out.String(), "Checking the local GUI build") {
				t.Fatal("no build notice")
			}
			if restarting {
				// The re-executed binary finds identical output, so it does not loop.
				if err := localGUIBuild(context.Background(), exe, exe, &out); err != nil {
					t.Fatalf("second launch: %v", err)
				}
			}
		})
	}
}

func TestLocalGUIIgnoresInstalledCopy(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "ouro")
	for _, mod := range []string{"", "module other\n", "module ouroboros\n"} {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := localGUIBuild(context.Background(), exe, exe, &out); err != nil || out.Len() != 0 {
			t.Fatalf("installed copy: %v, %s", err, &out)
		}
	}
}

func TestLocalGUINonGUICommands(t *testing.T) {
	for _, args := range [][]string{nil, {"tui"}, {"status"}, {"gui", "extra"}, {"--profile", "P", "help"}, {"--profile=P", "help"}} {
		if err := localGUI(context.Background(), args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// An explicit GUI launch checks the build of the checkout it runs from. It
// compares the build with the running image, not with the file at the path of
// the binary, which another launch may have replaced already: here that file
// is the new build already.
func TestLocalGUI(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	gone := errors.New("no executable")
	executable = func() (string, error) { return "", gone }
	if err := localGUI(context.Background(), []string{"gui"}); err != gone {
		t.Fatalf("no executable: %v", err)
	}
	exe := filepath.Join(checkout(t, stale), "ouro")
	if err := os.WriteFile(exe, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return exe, nil }
	for _, args := range [][]string{{"gui"}, {"fyne"}, {"--profile", "P", "fyne"}, {"--profile", "P", "desktop", "open"}} {
		err := localGUI(context.Background(), args)
		r, restarting := errors.AsType[*plugin.Restarting](err)
		if restarting != (runtime.GOOS == "linux") || (restarting && r.Exe != exe) || (!restarting && err != nil) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// What keeps the check from going on is an error: a running image that cannot
// be read, a checkout that cannot be written to, and a binary that cannot be
// replaced. Nothing is left behind.
func TestLocalGUIBuildFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("running image", func(t *testing.T) {
		root := checkout(t, stale)
		if err := localGUIBuild(ctx, filepath.Join(root, "ouro"), filepath.Join(root, "gone"), io.Discard); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing image: %v", err)
		}
	})
	t.Run("read-only checkout", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes anywhere")
		}
		root := checkout(t, stale)
		if err := os.Chmod(root, 0555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(root, 0755) })
		exe := filepath.Join(root, "ouro")
		if err := localGUIBuild(ctx, exe, exe, io.Discard); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("read-only checkout: %v", err)
		}
	})
	t.Run("binary is a directory", func(t *testing.T) {
		root := checkout(t, stale)
		exe := filepath.Join(root, "ouro")
		if err := os.Remove(exe); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(exe, 0755); err != nil {
			t.Fatal(err)
		}
		if _, ok := errors.AsType[*os.LinkError](localGUIBuild(ctx, exe, filepath.Join(root, "Makefile"), io.Discard)); !ok {
			t.Fatal("replaced a directory")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(root, ".ouro-gui-*")); len(leftovers) != 0 {
			t.Fatalf("staging directories left: %v", leftovers)
		}
	})
}

// A directory is no binary to hash.
func TestBinaryHashOfDirectory(t *testing.T) {
	if _, err := binaryHash(t.TempDir()); err == nil {
		t.Fatal("hashed a directory")
	}
}
