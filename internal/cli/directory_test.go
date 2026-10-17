package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/home"
	"ouroboros/internal/plugins/base"
	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

func TestStartDirectory(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", cwd)
	for _, name := range []string{"project", "project with spaces", "status", "help"} {
		if err := os.Mkdir(filepath.Join(cwd, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var directory, command string
	var commandArgs []string
	frontRuns := 0
	capture := func(ctx context.Context) error {
		directory = workdir.Of(ctx)
		frontRuns++
		return nil
	}
	p := plugin.New("test-front", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.FrontEnd{Name: "test-front", Run: capture})
		plugin.Add(ctx, plugin.Command{Name: "tui", Run: func(ctx context.Context, args []string) error {
			commandArgs = args
			return capture(ctx)
		}})
		plugin.Add(ctx, plugin.Command{Name: "status", Run: func(ctx context.Context, args []string) error {
			directory, command, commandArgs = workdir.Of(ctx), "status", args
			return nil
		}})
		return nil
	})
	app, err := NewWith(base.Plugin(home.Dir(t.TempDir()), plugin.Terminal{Out: &bytes.Buffer{}, Interactive: true}), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"cwd", nil, cwd},
		{"absolute", []string{filepath.Join(cwd, "project")}, filepath.Join(cwd, "project")},
		{"relative", []string{"project"}, filepath.Join(cwd, "project")},
		{"dot relative", []string{"./project"}, filepath.Join(cwd, "project")},
		{"parent relative", []string{"../" + filepath.Base(cwd) + "/project"}, filepath.Join(cwd, "project")},
		{"spaces", []string{"project with spaces"}, filepath.Join(cwd, "project with spaces")},
		{"home", []string{"~"}, cwd},
		{"home project", []string{"~/project with spaces"}, filepath.Join(cwd, "project with spaces")},
		{"tui cwd", []string{"tui"}, cwd},
		{"tui project", []string{"tui", "project with spaces"}, filepath.Join(cwd, "project with spaces")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			directory, commandArgs = "", nil
			before := frontRuns
			if err := app.Run(context.Background(), tt.args); err != nil {
				t.Fatal(err)
			}
			if directory != tt.want || frontRuns != before+1 || len(commandArgs) != 0 {
				t.Fatalf("directory %q, runs %d, args %v; want %q", directory, frontRuns-before, commandArgs, tt.want)
			}
			if got, err := os.Getwd(); err != nil || got != cwd {
				t.Fatalf("process directory changed: %q, %v", got, err)
			}
		})
	}
	for _, args := range [][]string{
		{filepath.Join(cwd, "missing")}, {"./missing"}, {"~/missing"}, {"./file"},
		{"project", "extra"}, {"tui", "missing"}, {"tui", ""}, {"tui", "project", "extra"},
	} {
		before := frontRuns
		if err := app.Run(context.Background(), args); err == nil || (!strings.Contains(err.Error(), "directory") && !strings.Contains(err.Error(), "DIRECTORY")) {
			t.Errorf("%v: expected directory error, got %v", args, err)
		}
		if frontRuns != before {
			t.Errorf("%v opened front end despite invalid directory", args)
		}
	}
	// Neither a command's arguments nor its context change, even if a
	// directory has its name. Built-in help also keeps its meaning.
	ctx := workdir.With(context.Background(), "unchanged")
	args := []string{"./missing", "project with spaces"}
	if err := app.Run(ctx, append([]string{"status"}, args...)); err != nil {
		t.Fatal(err)
	}
	if command != "status" || directory != "unchanged" || !reflect.DeepEqual(commandArgs, args) {
		t.Fatalf("command %q, directory %q, args %v", command, directory, commandArgs)
	}
	before := frontRuns
	if err := app.Run(ctx, []string{"help"}); err != nil || frontRuns != before {
		t.Fatalf("help opened a directory: %v", err)
	}
	if err := app.Run(ctx, []string{"missing-command"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command changed: %v", err)
	}
	// Project launches, like an argument-free UI, need no profile selection.
	app.profiles = map[home.Dir]*App{"one": app, "two": app}
	if err := app.Run(context.Background(), []string{"project"}); err != nil || directory != filepath.Join(cwd, "project") {
		t.Fatalf("multi-profile project: %q, %v", directory, err)
	}
	// No argument is no directory.
	if app.directoryArgument(nil) {
		t.Error("no argument taken for a directory")
	}
}

// A directory that cannot be told is an error, and no front end opens on
// another one instead: home without $HOME, and the current directory or
// one relative to it once it was removed.
func TestStartDirectoryThatCannotBeTold(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	t.Setenv("HOME", "")
	opened := 0
	p := plugin.New("test-front", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.FrontEnd{Name: "test-front", Run: func(context.Context) error { opened++; return nil }})
		return nil
	})
	app, err := NewWith(base.Plugin(home.Dir(t.TempDir()), plugin.Terminal{Out: &bytes.Buffer{}, Interactive: true}), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"~/project"}); err == nil || !strings.Contains(err.Error(), `project directory "~/project"`) {
		t.Errorf("home without $HOME: %v", err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "initial project directory") {
		t.Errorf("a current directory that was removed: %v", err)
	}
	if err := app.Run(context.Background(), []string{"./project"}); err == nil || !strings.Contains(err.Error(), `project directory "./project"`) {
		t.Errorf("relative to a directory that was removed: %v", err)
	}
	if opened != 0 {
		t.Errorf("opened %d times", opened)
	}
}
