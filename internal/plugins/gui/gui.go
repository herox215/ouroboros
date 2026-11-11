// Package gui contributes the explicit "gui" command, a window that chats
// with the thread of a profile. It does not replace the default terminal
// front end.
package gui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"ouroboros/plugin"
)

// profile is the window of one profile, handed to the frame alone.
type profile struct {
	status   func() (plugin.NodeStatus, error)
	sessions plugin.Sessions
	ctx      *plugin.Context // to read what the plugins contribute to a tool line
}

// Plugin contributes the window command. Like other front ends, it receives
// the narrow owner capability from the frame, never as a kernel service.
func Plugin(owner *plugin.Owner) plugin.Plugin {
	return plugin.New("gui", func(ctx *plugin.Context, n struct{ Sessions plugin.Sessions }) error {
		p := &profile{status: owner.Status, sessions: n.Sessions, ctx: ctx}
		plugin.Add(ctx, plugin.Command{Name: "gui", Help: "chat with the thread in a window", Run: p.run})
		return nil
	})
}

// run opens the window with the thread of the profile ouro runs for.
// Switching profiles is what "--profile" is for: the window shows one thread,
// and does not guess which of several profiles is meant.
func (p *profile) run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ouro [--profile PROFILE] gui")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	status, err := p.status()
	if err != nil {
		return fmt.Errorf("the profile of the window: %w", err)
	}
	name := status.Name
	if name == "" {
		name = "Not set up"
	}
	return show(ctx, p, name)
}

// show is replaced by tests so command tests never open a desktop window.
var show = showWindow

// handOver returns the plugin.Restarting that starts exe: ouro starts anew
// from the new build, with the same arguments, and so the window of the new
// process opens again for the same profile.
func handOver(exe string) error {
	return &plugin.Restarting{Exe: exe, Env: os.Environ()}
}
