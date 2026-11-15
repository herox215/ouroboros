// Package gui contributes the explicit "gui" command, a window that chats
// with the thread of a profile. It does not replace the default terminal
// front end.
package gui

import (
	"context"
	"errors"
	"fmt"
	"os"

	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// profile is the window of one profile, handed to the frame alone.
type profile struct {
	status   func() (plugin.NodeStatus, error)
	sessions plugin.Sessions
	dir      home.Dir        // the profile, where the pictures of the window are kept
	ctx      *plugin.Context // to read what the plugins contribute to a tool line
}

// Plugin contributes the window command, and the page of the thread on the
// network while the node serves. Like other front ends, it receives the narrow
// owner capability from the frame, never as a kernel service.
func Plugin(owner *plugin.Owner, dir home.Dir, files *guard.Guard) plugin.Plugin {
	return plugin.New("gui", func(ctx *plugin.Context, n struct{ Sessions plugin.Sessions }) error {
		p := &profile{status: owner.Status, sessions: n.Sessions, dir: dir, ctx: ctx}
		plugin.Add(ctx, plugin.Command{Name: "gui", Help: "chat with the thread in a window", Run: p.run})
		// The window is a channel for the pictures an agent shows, as the
		// gateways are: the shared gateway_send_image tool reaches it, so
		// there is no tool of the window's own.
		plugin.Add(ctx, pictureOutput(n.Sessions, files, pictures{dir: dir.Pictures()}))
		if owner == nil {
			return nil // no owner to ask who the page is of
		}
		// The page runs where the node runs, not in a front end that ends
		// with the terminal: only the node serving starts it.
		front := newWebFront(dir, p)
		plugin.Add(ctx, plugin.Serving{
			Start: front.Serve,
			// Stop is what turns the page off: the watch that reconciles it
			// with the configuration ends with it, so that a node that stops
			// does not serve, and hands its port to no one in particular.
			Stop: front.Close,
		})
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
