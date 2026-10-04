package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/node"
)

// defaultCommand runs when ouro is started in a terminal without a command.
const defaultCommand = "tui"

// basePlugin provides what every other plugin builds on: the settings, the
// home directory of the node, the console, the control of the local node and
// the dispatcher that runs commands.
func basePlugin(s Settings, t Terminal) kernel.Plugin {
	return kernel.Plugin{Name: "base", Start: func(ctx *kernel.Context) error {
		dir := home.Dir(s.Home)
		term := newConsole(t)
		kernel.Provide(ctx, s)
		kernel.Provide(ctx, t)
		kernel.Provide(ctx, dir)
		kernel.Provide(ctx, term)
		kernel.Provide(ctx, node.Local{Home: dir})
		kernel.Provide(ctx, &dispatcher{ctx: ctx, term: term, home: dir, interactive: t.Interactive})
		return nil
	}}
}

// dispatcher runs the commands the plugins contributed.
type dispatcher struct {
	ctx         *kernel.Context
	term        *console
	home        home.Dir
	interactive bool
}

// run executes the command given by args (without the program name).
func (d *dispatcher) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		if _, ok := d.find(defaultCommand); !ok || !d.interactive {
			return d.usage()
		}
		args = []string{defaultCommand}
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
	return c.run(ctx, rest)
}

func (d *dispatcher) find(name string) (command, bool) {
	for _, c := range kernel.All[command](d.ctx) {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// usage lists the commands.
func (d *dispatcher) usage() error {
	var b strings.Builder
	b.WriteString("ouro – tiny A2A agent node for any OpenAI-compatible LLM API\n\n")
	line := func(synopsis, help string) { fmt.Fprintf(&b, "  ouro %-19s%s\n", synopsis, help) }
	if c, ok := d.find(defaultCommand); ok {
		line("", c.help+" (default)")
	}
	for _, c := range kernel.All[command](d.ctx) {
		line(strings.TrimSpace(c.name+" "+c.args), c.help)
	}
	_, err := io.WriteString(d.term.out, b.String())
	return err
}
