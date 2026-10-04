package base_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/plugins/base"
	"ouroboros/plugin"
)

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}} {
		out, err := clitest.Run(t, clitest.Settings(t), "", args...)
		if err != nil || !strings.Contains(out, "ouro start") {
			t.Fatalf("%v: got %q, %v", args, out, err)
		}
	}
}

// Without a terminal, ouro with no command prints the usage instead of TUI.
func TestNoArgsWithoutTerminalShowsUsage(t *testing.T) {
	out, err := clitest.Run(t, clitest.Settings(t), "")
	if err != nil || !strings.Contains(out, "ouro start") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestUnknownCommand(t *testing.T) {
	if _, err := clitest.Run(t, clitest.Settings(t), "", "fly"); err == nil {
		t.Fatal("want error")
	}
}

// With a terminal, ouro without a command opens the terminal UI, which gives
// up here: there is no terminal to draw on.
func TestNoArgsWithTerminalOpensTheUI(t *testing.T) {
	app, out := clitest.NewAppOn(t, clitest.Settings(t), cli.Terminal{Interactive: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx, nil); err == nil || strings.Contains(out.String(), "ouro start") {
		t.Fatalf("got %q, %v", out.String(), err)
	}
}

// The usage names the default command only if a plugin contributed it.
func TestUsageWithoutDefaultCommand(t *testing.T) {
	out, err := clitest.Run(t, clitest.Settings(t), "", "-h")
	if err != nil || !strings.Contains(out, "chat with your agent in the terminal UI (default)") {
		t.Fatalf("got %q, %v", out, err)
	}

	var printed bytes.Buffer
	hello := plugin.New("hello", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Command{Name: "hello", Args: "NAME", Help: "say hello", Run: func(_ context.Context, args []string) error {
			printed.WriteString("hello " + strings.Join(args, " "))
			return nil
		}})
		return nil
	})
	dir := home.Dir(filepath.Join(t.TempDir(), "home"))
	app, err := cli.NewWith(base.Plugin(dir, plugin.Terminal{In: strings.NewReader(""), Out: &printed, Interactive: true}), hello)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), nil); err != nil || strings.Contains(printed.String(), "(default)") || !strings.Contains(printed.String(), "  ouro hello NAME         say hello\n") {
		t.Fatalf("got %q, %v", printed.String(), err)
	}
	// A command gets its arguments, and finds the home of the node made.
	printed.Reset()
	if err := app.Run(context.Background(), []string{"hello", "big", "world"}); err != nil || printed.String() != "hello big world" {
		t.Fatalf("got %q, %v", printed.String(), err)
	}
	if info, err := os.Stat(string(dir)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home: %v, %v", info, err)
	}
}

// A home that cannot be made fails the command before it runs.
func TestHomeThatCannotBeMade(t *testing.T) {
	if out, err := clitest.Run(t, clitest.Unreadable(t), "", "status"); err == nil || out != "" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestWithHint(t *testing.T) {
	if err := base.WithHint(config.ErrIncomplete); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "run: ouro start") {
		t.Fatalf("got %v", err)
	}
	other := errors.New("other")
	if err := base.WithHint(other); err != other {
		t.Fatalf("got %v", err)
	}
	if base.WithHint(nil) != nil {
		t.Fatal("no error needs no hint")
	}
}
