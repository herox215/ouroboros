// Command ouro runs and talks to Ouroboros A2A nodes.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"ouroboros/internal/cli"
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
	if err != nil {
		fail(err)
	}
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
