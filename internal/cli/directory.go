package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ouroboros/internal/plugins/base"
	"ouroboros/internal/workdir"
)

// directoryArgument distinguishes a project path from a command. Commands
// keep their meaning even when a directory has the same name.
func (a *App) directoryArgument(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if a.kernel != nil {
		if d, ok := Service[*base.Dispatcher](a); ok && d.HasCommand(args[0]) {
			return false
		}
	}
	path := args[0]
	if filepath.IsAbs(path) || path == "~" || strings.HasPrefix(path, "~/") ||
		path == "." || path == ".." || strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// startDirectory passes the initial project to front ends without changing
// the process directory or the workspace of a profile.
func (a *App) startDirectory(ctx context.Context, args []string) (context.Context, []string, error) {
	var path string
	switch {
	case len(args) == 0:
	case args[0] == "tui" || args[0] == "gui":
		if len(args) > 2 {
			return ctx, args, fmt.Errorf("usage: ouro %s [DIRECTORY]", args[0])
		}
		if len(args) == 2 {
			path = args[1]
			if path == "" {
				return ctx, args, fmt.Errorf("project directory must not be empty")
			}
		}
		args = args[:1]
	case a.directoryArgument(args):
		if len(args) != 1 {
			return ctx, args, fmt.Errorf("usage: ouro [DIRECTORY] (a project directory takes no further arguments)")
		}
		path, args = args[0], nil
	default:
		return ctx, args, nil
	}
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ctx, args, fmt.Errorf("initial project directory: %w", err)
		}
		path = cwd
	}
	directory, err := projectDirectory(path)
	if err != nil {
		return ctx, args, err
	}
	return workdir.With(ctx, directory), args, nil
}

func projectDirectory(path string) (string, error) {
	original := path
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("project directory %q: %w", original, err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("project directory %q: %w", original, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("project directory %q: %w", original, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project directory %q: not a directory", original)
	}
	return path, nil
}
