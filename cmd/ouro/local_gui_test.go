package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/plugin"
)

func TestLocalGUIBuild(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		restart, fail    bool
	}{
		{name: "current", body: "cp ouro \"$dest\"", want: "old"},
		{name: "stale", body: "printf new > \"$dest\"; chmod 755 \"$dest\"", want: "new", restart: true},
		{name: "concurrent replacement", body: "printf new > ouro; printf new > \"$dest\"; chmod 755 \"$dest\"", want: "new", restart: true},
		{name: "failure", body: "printf broken > \"$dest\"; exit 1", want: "old", fail: true},
		{name: "missing output", body: "true", want: "old", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			put("tools/make", "#!/bin/sh\n[ \"$1\" = build ] || exit 2\ndest=\"${2#BIN=}\"\n"+tc.body+"\n", 0755)
			t.Setenv("PATH", filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
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
