// Package tui is the plugin that contributes the terminal UI: the command
// "tui" and the front end that ouro without a command opens.
//
// It is written against ouroboros/plugin like any other plugin. What it shows
// besides the chat is what the plugins contributed: their screens
// (plugin.Screen), their keys (plugin.Key) and what is shown of a tool call
// (plugin.ToolView).
package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// name is the command that opens the UI, and what the front end is called.
const name = "tui"

// needs are the services the plugin uses.
type needs struct {
	Console   *plugin.Console
	Assistant *plugin.Assistant
}

// Plugin contributes the terminal UI as the command "tui" and as the front
// end of ouro.
//
// The UI is where the owner starts and stops the node and decides whom it
// trusts. No service offers that: it is what owner does, a part of the frame
// of ouro that is handed to this plugin where the plugins are listed.
func Plugin(owner *plugin.Owner) plugin.Plugin {
	return plugin.New(name, func(ctx *plugin.Context, n needs) error {
		u := &ui{Owner: owner, ctx: ctx, assistant: n.Assistant}
		const help = "chat with your agent in the terminal UI"
		plugin.Add(ctx, plugin.Command{Name: name, Help: help, Run: func(c context.Context, _ []string) error {
			if !n.Console.Interactive {
				return errors.New("tui needs a terminal")
			}
			return u.run(c)
		}})
		plugin.Add(ctx, plugin.FrontEnd{Name: name, Help: help, Run: u.run})
		return nil
	})
}

// ui is the terminal UI and what it controls: it implements tui.Backend with
// what the owner does and the assistant of the owner.
type ui struct {
	*plugin.Owner
	ctx       *plugin.Context // to read what the plugins contributed to the UI
	assistant *plugin.Assistant
}

// show runs the UI m on the terminal until the user quits; tests replace it
// to use the UI without a terminal.
var show = func(ctx context.Context, m *tui.Model) error {
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// run shows the terminal UI until the user quits, with the screens and keys
// the plugins contributed. While it is open the node is online: the UI starts
// it, and a node that was not running before is stopped again afterwards. An
// answer still on its way when the user quits is stopped, and the log says
// so before the UI is gone.
func (u *ui) run(ctx context.Context) error {
	wasRunning := u.Running()
	err := show(ctx, tui.New(u, plugin.All[plugin.Screen](u.ctx), plugin.All[plugin.Key](u.ctx)))
	u.assistant.Stop()
	if !wasRunning {
		err = errors.Join(err, u.Stop())
	}
	return err
}

// Start starts the node; one that was not set up is told how to get there in
// the UI.
func (u *ui) Start() error {
	err := u.Owner.Start()
	if errors.Is(err, plugin.ErrNotSetUp) {
		return fmt.Errorf("%w, press c", err)
	}
	return err
}

// Ask lets the assistant answer. Of the tools it calls on the way, the UI is
// told what the plugins want shown: a call without a plugin.ToolView is not
// reported.
func (u *ui) Ask(ctx context.Context, contextID, text string, step func(tui.Step)) (string, error) {
	reply, err := u.assistant.Ask(ctx, contextID, text, func(s plugin.Step) {
		if line := plugin.ToolLine(u.ctx, s); line != "" {
			step(tui.Step{Text: line, Done: s.Done})
		}
	})
	if errors.Is(err, plugin.ErrNotSetUp) {
		return reply, fmt.Errorf("%w, run: ouro start", err)
	}
	return reply, err
}

func (u *ui) Contacts() (tui.Contacts, error) {
	incoming, outgoing, err := u.Requests()
	if err != nil {
		return tui.Contacts{}, err
	}
	proposals, err := u.Proposals()
	if err != nil {
		return tui.Contacts{}, err
	}
	return tui.Contacts{Incoming: incoming, Outgoing: outgoing, Proposals: proposals}, nil
}
