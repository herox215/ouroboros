package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/kernel"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
	"ouroboros/internal/tui"
)

// tuiPlugin contributes the terminal UI as the command "tui", which is also
// what ouro without a command opens.
//
// Another front end (a web UI, an API) is a plugin like this one: a small
// adapter over node.Local, assistant.Assistant and peers.Network, plus the
// command that runs it. It needs no logic of its own.
func tuiPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "tui", Start: func(ctx *kernel.Context) error {
		var err error
		t := use[Terminal](ctx, &err)
		u := &ui{
			local:     use[node.Local](ctx, &err),
			assistant: use[*assistant.Assistant](ctx, &err),
			network:   use[*peers.Network](ctx, &err),
		}
		if err != nil {
			return err
		}
		kernel.Provide(ctx, u)
		kernel.Add(ctx, command{defaultCommand, "", "chat with your agent in the terminal UI", func(c context.Context, _ []string) error {
			if !t.Interactive {
				return errors.New("tui needs a terminal")
			}
			return u.run(c)
		}})
		return nil
	}}
}

// ui is the terminal UI and what it controls: it implements tui.Backend by
// handing every request to the part that is responsible for it.
type ui struct {
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
	return reply, withHint(err)
}

func (u *ui) Contacts() (tui.Contacts, error) {
	data, err := u.network.Contacts.Snapshot()
	return tui.Contacts{Incoming: data.Incoming, Outgoing: data.Outgoing}, err
}

func (u *ui) Accept(publicKey string) error {
	return u.network.Accept(context.Background(), publicKey)
}

func (u *ui) Reject(publicKey string) error {
	return u.network.Reject(context.Background(), publicKey)
}

// SetupCommand runs the interactive setup, which starts the node afterwards.
func (u *ui) SetupCommand() (*exec.Cmd, error) {
	exe, err := node.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "start", "--setup"), nil
}
