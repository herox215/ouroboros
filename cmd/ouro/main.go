// Command ouro runs and talks to Ouroboros A2A nodes.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"ouroboros/internal/cli"
	"ouroboros/plugin"
)

func main() {
	settings, err := cli.SettingsFromEnv()
	if err != nil {
		fail(err)
	}
	terminal := cli.Terminal{In: os.Stdin, Out: os.Stdout}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		terminal.Interactive = true
		terminal.ReadSecret = readPassword
	}
	app, err := cli.New(settings, terminal)
	if err == nil {
		err = app.Run(context.Background(), os.Args[1:])
	}
	if err = restart(err); err != nil {
		fail(err)
	}
}

// execute replaces the process by the program at path; tests replace it.
var execute = syscall.Exec

// restart runs the binary in place of this process, with its arguments,
// when err is the front end asking to be started anew from a new build: it
// goes on on this terminal. Any other err is returned as it is.
func restart(err error) error {
	if r, ok := errors.AsType[*plugin.Restarting](err); ok {
		return execute(r.Exe, os.Args, r.Env)
	}
	return err
}

func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(b)), err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "✗", err)
	os.Exit(1)
}
