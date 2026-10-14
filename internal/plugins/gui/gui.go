// Package gui contributes the explicit "gui" command, a Fyne demo with a
// profile picker. It does not replace the default terminal front end.
package gui

import (
	"context"
	"errors"
	"fmt"

	"ouroboros/plugin"
)

// Profile is the GUI of one profile, handed to the frame alone.
type Profile struct {
	status   func() (plugin.NodeStatus, error)
	profiles []*Profile
}

// Plugin contributes the demo command. Like other front ends, it receives
// the narrow owner capability from the frame, never as a kernel service.
func Plugin(owner *plugin.Owner, profile func(*Profile)) plugin.Plugin {
	return plugin.New("gui", func(ctx *plugin.Context, _ struct{}) error {
		p := &Profile{status: owner.Status}
		plugin.Add(ctx, plugin.Command{Name: "gui", Help: "open the Fyne demo with a profile picker", Run: p.run})
		if profile != nil {
			profile(p)
		}
		return nil
	})
}

// Together lets each GUI show all profiles whose GUI plugin is enabled.
// A disabled plugin hands over nil and is not shown.
func Together(profiles []*Profile) {
	all := make([]*Profile, 0, len(profiles))
	for _, p := range profiles {
		if p != nil {
			all = append(all, p)
		}
	}
	for _, p := range all {
		p.profiles = all
	}
}

type choice struct{ name, id string }
type page struct {
	choices  []choice
	selected int
}

func (p *Profile) page() (page, error) {
	profiles := p.profiles
	if len(profiles) == 0 {
		profiles = []*Profile{p}
	}
	var out page
	for i, profile := range profiles {
		status, err := profile.status()
		if err != nil {
			return page{}, fmt.Errorf("GUI profile %d: %w", i+1, err)
		}
		name := status.Name
		if name == "" {
			name = "Not set up"
		}
		out.choices = append(out.choices, choice{name: name, id: status.ID})
		if profile == p {
			out.selected = i
		}
	}
	return out, nil
}

func (p *Profile) run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ouro [--profile PROFILE] gui")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	page, err := p.page()
	if err != nil {
		return err
	}
	return show(ctx, page)
}

// show is replaced by tests so command tests never open a desktop window.
var show = showWindow
