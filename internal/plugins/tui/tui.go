// Package tui is the plugin that contributes the terminal UI.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
	"ouroboros/internal/plugins/base"
	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	Console   *plugin.Console
	Assistant *assistant.Assistant
}

// Plugin contributes the terminal UI as the command "tui", which is also what
// ouro without a command opens (base.DefaultCommand).
//
// The UI is where the owner decides whom the node trusts. For that it is
// given what no service offers: dir, the home of the node, and network with
// the contacts in it. They are part of the frame of ouro, handed to this
// plugin where the plugins are listed.
func Plugin(dir home.Dir, network *peers.Network) plugin.Plugin {
	return plugin.New("tui", func(ctx *plugin.Context, n needs) error {
		u := &ui{home: dir, local: node.Local{Home: dir}, assistant: n.Assistant, network: network}
		plugin.Add(ctx, plugin.Command{Name: base.DefaultCommand, Help: "chat with your agent in the terminal UI", Run: func(c context.Context, _ []string) error {
			if !n.Console.Interactive {
				return errors.New("tui needs a terminal")
			}
			return u.run(c)
		}})
		return nil
	})
}

// ui is the terminal UI and what it controls: it implements tui.Backend by
// handing every request to the part that is responsible for it.
type ui struct {
	home      home.Dir
	local     node.Local
	assistant *assistant.Assistant
	network   *peers.Network
}

// run shows the terminal UI until the user quits. While it is open the node is
// online: the UI starts it, and a node that was not running before is stopped
// again afterwards.
func (u *ui) run(ctx context.Context) error {
	_, wasRunning := u.local.Running()
	_, err := tea.NewProgram(tui.New(u), tea.WithContext(ctx)).Run()
	if !wasRunning {
		_, stopErr := u.local.Stop()
		err = errors.Join(err, stopErr)
	}
	return err
}

func (u *ui) Status() (tui.Status, error) {
	s, err := u.local.Status()
	return tui.Status(s), err
}

func (u *ui) Start() error {
	_, err := u.local.Start()
	if errors.Is(err, config.ErrIncomplete) {
		return fmt.Errorf("%w, press c", err)
	}
	return err
}

func (u *ui) Stop() error {
	_, err := u.local.Stop()
	return err
}

func (u *ui) Ask(ctx context.Context, contextID, text string, step func(tui.Step)) (string, error) {
	reply, err := u.assistant.Ask(ctx, contextID, text, func(s assistant.Step) { step(tui.Step(s)) })
	return reply, base.WithHint(err)
}

func (u *ui) Contacts() (tui.Contacts, error) {
	data, err := u.network.Contacts.Snapshot()
	return tui.Contacts{Incoming: data.Incoming, Outgoing: data.Outgoing}, err
}

func (u *ui) Accept(ctx context.Context, publicKey string) error {
	return u.decided("accept", publicKey, u.network.Accept(ctx, publicKey))
}

func (u *ui) Reject(ctx context.Context, publicKey string) error {
	return u.decided("reject", publicKey, u.network.Reject(ctx, publicKey))
}

// decided hands on the outcome of a decision on the connection request of
// publicKey. Of one that could not be stored the UI only says that it failed:
// the cause goes to the log of the node.
func (u *ui) decided(action, publicKey string, err error) error {
	if errors.Is(err, peers.ErrNotSaved) {
		u.home.Log("%s the connection request of %s: %v", action, publicKey, err)
		return tui.ErrNotSaved
	}
	return err
}

// SetupCommand runs the interactive setup, which starts the node afterwards.
func (u *ui) SetupCommand() (*exec.Cmd, error) {
	exe, err := u.local.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "start", "--setup"), nil
}
