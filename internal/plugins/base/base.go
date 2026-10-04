// Package base is the plugin every other plugin builds on: it provides the
// console and the dispatcher that runs the commands the plugins contributed.
package base

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// DefaultCommand runs when ouro is started in a terminal without a command.
const DefaultCommand = "tui"

// services are what the plugin provides.
type services struct {
	Console    *plugin.Console
	Dispatcher *Dispatcher
}

// Plugin provides the console on t and the dispatcher of the commands of the
// node in dir.
func Plugin(dir home.Dir, t plugin.Terminal) plugin.Plugin {
	return plugin.NewService("base", func(ctx *plugin.Context, _ struct{}) (services, error) {
		term := plugin.NewConsole(t)
		return services{Console: term, Dispatcher: &Dispatcher{ctx: ctx, term: term, home: dir}}, nil
	})
}

// Dispatcher runs the commands the plugins contributed.
type Dispatcher struct {
	ctx  *plugin.Context
	term *plugin.Console
	home home.Dir
}

// Run executes the command given by args (without the program name).
func (d *Dispatcher) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		if !d.HasDefault() {
			return d.usage()
		}
		args = []string{DefaultCommand}
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "--help":
		return d.usage()
	}
	c, ok := d.find(name)
	if !ok {
		return fmt.Errorf("unknown command %q (see: ouro help)", name)
	}
	if err := d.home.Create(); err != nil {
		return err
	}
	return c.Run(ctx, rest)
}

// HasDefault reports whether ouro without a command runs DefaultCommand: a
// plugin contributed it, and the user sits at a terminal.
func (d *Dispatcher) HasDefault() bool {
	_, ok := d.find(DefaultCommand)
	return ok && d.term.Interactive
}

func (d *Dispatcher) find(name string) (plugin.Command, bool) {
	for _, c := range plugin.All[plugin.Command](d.ctx) {
		if c.Name == name {
			return c, true
		}
	}
	return plugin.Command{}, false
}

// usage lists the commands.
func (d *Dispatcher) usage() error {
	var b strings.Builder
	b.WriteString("ouro – tiny A2A agent node for any OpenAI-compatible LLM API\n\n")
	line := func(synopsis, help string) { fmt.Fprintf(&b, "  ouro %-19s%s\n", synopsis, help) }
	if c, ok := d.find(DefaultCommand); ok {
		line("", c.Help+" (default)")
	}
	for _, c := range plugin.All[plugin.Command](d.ctx) {
		line(strings.TrimSpace(c.Name+" "+c.Args), c.Help)
	}
	_, err := io.WriteString(d.term.Out, b.String())
	return err
}

// WithHint tells how to get past a node that was not set up yet.
func WithHint(err error) error {
	if errors.Is(err, config.ErrIncomplete) {
		return fmt.Errorf("%w, run: ouro start", err)
	}
	return err
}
