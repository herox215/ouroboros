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
		return d.open(ctx)
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

// open runs ouro without a command: it opens the front end a plugin
// contributed. Without one, or without a terminal, it prints the usage; with
// more than one it fails, see plugin.FrontEndFor.
func (d *Dispatcher) open(ctx context.Context) error {
	front, err := plugin.FrontEndFor(d.ctx, d.term)
	if errors.Is(err, plugin.ErrNoFrontEnd) {
		return d.usage()
	}
	if err != nil {
		return err
	}
	if err := d.home.Create(); err != nil {
		return err
	}
	return front.Run(ctx)
}

func (d *Dispatcher) find(name string) (plugin.Command, bool) {
	for _, c := range plugin.All[plugin.Command](d.ctx) {
		if c.Name == name {
			return c, true
		}
	}
	return plugin.Command{}, false
}

// usageColumn is the width of the synopsis of a command in the usage.
const usageColumn = 19

// usage lists the commands.
func (d *Dispatcher) usage() error {
	var b strings.Builder
	b.WriteString("ouro – tiny A2A agent node for any OpenAI-compatible LLM API\n\n")
	// A synopsis too long for its column puts its help on a line of its own,
	// below it in the column of the others.
	line := func(synopsis, help string) {
		if len(synopsis) >= usageColumn {
			fmt.Fprintf(&b, "  ouro %s\n  %-*s", synopsis, usageColumn+5, "")
		} else {
			fmt.Fprintf(&b, "  ouro %-*s", usageColumn, synopsis)
		}
		fmt.Fprintf(&b, "%s\n", help)
	}
	if fronts := plugin.All[plugin.FrontEnd](d.ctx); len(fronts) == 1 {
		line("", fronts[0].Help+" (default)")
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
