// Package gui contributes the explicit "gui" command, a window that chats
// with the thread of a profile. It does not replace the default terminal
// front end.
package gui

import (
	"context"
	"errors"
	"fmt"

	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// profile is the window of one profile, handed to the frame alone.
type profile struct {
	status func() (plugin.NodeStatus, error)
	// rename calls the profile, and with it the agent of its thread, a new
	// name; nil where the window has no owner to change it through.
	rename func(name string) error
	// models lists the accesses of the node with their models; nil where the
	// window has no owner to ask.
	models func(ctx context.Context) ([]plugin.AccessModels, error)
	// llms changes the accesses of the node and what its scopes answer with,
	// see providers.go; nil where the window has no owner to ask.
	llms llmOwner
	// gateways lists the gateways the plugins contribute, read when they are
	// shown, see gateways.go; nil where the window has no owner to change
	// them through.
	gateways func() []plugin.Gateway
	logins   logins // the sign-ins to providers on their way
	sessions plugin.Sessions
	dir      home.Dir        // the profile, where the pictures of the window are kept
	ctx      *plugin.Context // to read what the plugins contribute to a tool line
}

// watchers is the sessions of a node: it tells which front ends watch a
// session, see session.Watching.
type watchers interface {
	Watching(id, source string) int
	ShowsThread(id string) bool
}

// windowShows reports whether a window of ouro shows the thread now. The window
// runs in a process of its own and says which front end it is when it watches
// the thread; a process that keeps no sessions of its own has no window.
func windowShows(sessions watchers) bool {
	return sessions != nil && sessions.Watching(plugin.ThreadID, plugin.SourceWindow) > 0
}

// shownBy is what the provider asks: a picture the call of ctx shows now would
// be seen by a window of ouro that watches the thread, or by the page of it,
// which page tells and is nil for a process that serves no page. Both show the
// thread alone: the conversation of the call is the thread, or works for it.
func shownBy(sessions watchers, page func() bool) func(ctx context.Context) bool {
	return func(ctx context.Context) bool {
		if sessions == nil || !sessions.ShowsThread(plugin.ContextID(ctx)) {
			return false
		}
		return windowShows(sessions) || (page != nil && page())
	}
}

// Plugin contributes the window command, and the page of the thread on the
// network while the node serves. Like other front ends, it receives the narrow
// owner capability from the frame, never as a kernel service.
func Plugin(owner *plugin.Owner, dir home.Dir, files *guard.Guard) plugin.Plugin {
	// The sessions of this process are what tell whether a window of ouro
	// watches the thread; the socket client of a front end is not, see below.
	return plugin.New("gui", func(ctx *plugin.Context, n struct {
		Sessions plugin.Sessions
		Service  *session.Service
	}) error {
		p := &profile{status: owner.Status, sessions: n.Sessions, dir: dir, ctx: ctx}
		if owner != nil {
			// The name is a setting like any other: changed as the settings of
			// the terminal UI change it, so that it is checked the same way.
			p.rename = func(name string) error {
				_, err := owner.Change(plugin.Setting{Kind: plugin.SetName, Value: name}, nil, nil)
				return err
			}
			p.models = func(c context.Context) ([]plugin.AccessModels, error) {
				return owner.Models(c, plugin.All[plugin.Provider](ctx))
			}
			p.llms = ownerLLMs{Owner: owner, providers: func() []plugin.Provider { return plugin.All[plugin.Provider](ctx) }}
			p.gateways = func() []plugin.Gateway { return plugin.All[plugin.Gateway](ctx) }
		}
		plugin.Add(ctx, plugin.Command{Name: "gui", Help: "chat with the thread in a window", Run: p.run})
		// The window shows the pictures an agent makes, as the gateways show
		// them: the shared gateway_send_image tool reaches it, so there is no
		// tool of the window's own. A picture shown now would be seen while a
		// window of ouro watches the thread, or while the page of the thread is
		// served here, and the provider says so with Available: a gateway that
		// is there stays the only one the tool can mean for a profile that
		// shows nothing.
		shown := shownBy(n.Service, nil)
		if owner != nil {
			// The page runs where the node runs, not in a front end that ends
			// with the terminal: only the node serving starts it.
			front := newWebFront(dir, p)
			shown = shownBy(n.Service, front.serves)
			plugin.Add(ctx, plugin.Serving{
				Start: front.Serve,
				// Stop is what turns the page off: the watch that reconciles it
				// with the configuration ends with it, so that a node that stops
				// does not serve, and hands its port to no one in particular.
				Stop: front.Close,
			})
		}
		plugin.Add(ctx, pictureOutput(n.Sessions, files, pictures{dir: dir.Pictures()}, shown))
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
