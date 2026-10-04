package base_test

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// frontEnd is a plugin called name that contributes a front end of that name,
// which notes in ran that it was opened.
func frontEnd(name string, ran *[]string) plugin.Plugin {
	return plugin.New(name, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.FrontEnd{Name: name, Help: "open " + name, Run: func(context.Context) error {
			*ran = append(*ran, name)
			return nil
		}})
		return nil
	})
}

// ouro is ouro made of the base plugin and plugins, for a user at a terminal
// or not, with the home of its node and what it prints.
func ouro(t *testing.T, interactive bool, plugins ...plugin.Plugin) (*cli.App, home.Dir, *bytes.Buffer) {
	t.Helper()
	var printed bytes.Buffer
	dir := home.Dir(filepath.Join(t.TempDir(), "home"))
	term := plugin.Terminal{In: strings.NewReader(""), Out: &printed, Interactive: interactive}
	app, err := cli.NewWith(append([]plugin.Plugin{base.Plugin(dir, term)}, plugins...)...)
	if err != nil {
		t.Fatal(err)
	}
	return app, dir, &printed
}

// Without a front end ouro prints the usage when it is started without a
// command, and the usage names no default.
func TestUsageWithoutFrontEnd(t *testing.T) {
	out, err := clitest.Run(t, clitest.Settings(t), "", "-h")
	if err != nil || !strings.Contains(out, "chat with your agent in the terminal UI (default)") {
		t.Fatalf("got %q, %v", out, err)
	}

	hello := plugin.New("hello", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Command{Name: "hello", Args: "NAME", Help: "say hello", Run: func(_ context.Context, args []string) error {
			return errors.New("hello " + strings.Join(args, " "))
		}})
		return nil
	})
	app, dir, printed := ouro(t, true, hello)
	if err := app.Run(context.Background(), nil); err != nil || strings.Contains(printed.String(), "(default)") || !strings.Contains(printed.String(), "  ouro hello NAME         say hello\n") {
		t.Fatalf("got %q, %v", printed.String(), err)
	}
	if _, err := os.Stat(string(dir)); err == nil {
		t.Fatal("the usage needs no home")
	}
	// A command gets its arguments, and finds the home of the node made.
	if err := app.Run(context.Background(), []string{"hello", "big", "world"}); err == nil || err.Error() != "hello big world" {
		t.Fatalf("got %v", err)
	}
	if info, err := os.Stat(string(dir)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home: %v, %v", info, err)
	}
}

// The front end a plugin contributed is what ouro without a command opens,
// for a user at a terminal.
func TestFrontEndOpensWithoutCommand(t *testing.T) {
	var ran []string
	app, dir, printed := ouro(t, true, frontEnd("web", &ran))
	if err := app.Run(context.Background(), nil); err != nil || len(ran) != 1 || printed.Len() != 0 {
		t.Fatalf("ran %v, printed %q, %v", ran, printed, err)
	}
	if _, err := os.Stat(string(dir)); err != nil {
		t.Fatalf("the front end should find the home of the node made: %v", err)
	}
	if err := app.Run(context.Background(), []string{"help"}); err != nil || !strings.Contains(printed.String(), "  ouro                    open web (default)\n") {
		t.Fatalf("got %q, %v", printed.String(), err)
	}

	// Pipes get the usage.
	app, _, printed = ouro(t, false, frontEnd("web", &ran))
	if err := app.Run(context.Background(), nil); err != nil || len(ran) != 1 || !strings.Contains(printed.String(), "open web (default)") {
		t.Fatalf("ran %v, printed %q, %v", ran, printed, err)
	}
}

// ouro does not pick one of several front ends: it says which there are.
func TestSeveralFrontEnds(t *testing.T) {
	var ran []string
	app, _, printed := ouro(t, true, frontEnd("web", &ran), frontEnd("tui", &ran))
	err := app.Run(context.Background(), nil)
	if !errors.Is(err, plugin.ErrFrontEnds) || !strings.Contains(err.Error(), "ouro web, ouro tui") || len(ran) != 0 || printed.Len() != 0 {
		t.Fatalf("ran %v, printed %q, %v", ran, printed, err)
	}
	// None of them is the default.
	if err := app.Run(context.Background(), []string{"help"}); err != nil || strings.Contains(printed.String(), "(default)") {
		t.Fatalf("got %q, %v", printed.String(), err)
	}
	// Without a terminal none would open anyway.
	app, _, printed = ouro(t, false, frontEnd("web", &ran), frontEnd("tui", &ran))
	if err := app.Run(context.Background(), nil); err != nil || !strings.Contains(printed.String(), "ouro –") {
		t.Fatalf("got %q, %v", printed.String(), err)
	}
}

// A home that cannot be made fails the command before it runs, and the front
// end before it opens.
func TestHomeThatCannotBeMade(t *testing.T) {
	if out, err := clitest.Run(t, clitest.Unreadable(t), "", "status"); err == nil || out != "" {
		t.Fatalf("got %q, %v", out, err)
	}
	var ran []string
	term := plugin.Terminal{In: strings.NewReader(""), Out: io.Discard, Interactive: true}
	app, err := cli.NewWith(base.Plugin(home.Dir(clitest.Unreadable(t).Home), term), frontEnd("web", &ran))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), nil); err == nil || len(ran) != 0 {
		t.Fatalf("ran %v, %v", ran, err)
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
