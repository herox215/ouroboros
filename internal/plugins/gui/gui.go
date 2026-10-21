// Package gui contributes the explicit "gui" command, a session chat window
// with a profile picker. It does not replace the default terminal front end.
package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

// Profile is the GUI of one profile, handed to the frame alone.
type Profile struct {
	owner    *plugin.Owner
	status   func() (plugin.NodeStatus, error)
	sessions plugin.Sessions
	ctx      *plugin.Context
	archive  archiveAccess
	profiles []*Profile
}

// Plugin contributes the session browser command. Like other front ends, it receives
// the narrow owner capability from the frame, never as a kernel service.
func Plugin(owner *plugin.Owner, profile func(*Profile)) plugin.Plugin {
	return plugin.New("gui", func(ctx *plugin.Context, n struct{ Sessions plugin.Sessions }) error {
		p := &Profile{owner: owner, status: owner.Status, sessions: n.Sessions, ctx: ctx}
		plugin.Add(ctx, plugin.Command{Name: "gui", Help: "chat in a window with a profile picker", Run: p.run})
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

type archiveAccess struct {
	page    func(string, int) (plugin.ArchivedPage, error)
	records func(string) ([]plugin.ArchivedRecord, error)
}

type choice struct{ name, id string }
type page struct {
	choices   []choice
	archives  []archiveAccess // legacy archive tests only
	profiles  []*Profile
	selected  int
	session   string // the session the window goes on with, "" to show the thread
	directory string // the start directory, as in the terminal UI
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
		out.archives = append(out.archives, profile.archive)
		out.profiles = append(out.profiles, profile)
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
	page.directory = workdir.Of(ctx)
	going, err := resumed()
	if err != nil {
		return err
	}
	if going != nil {
		page.session = going.Session
		for i, c := range page.choices {
			if c.id == going.Profile {
				page.selected = i
			}
		}
	}
	return show(ctx, page)
}

// show is replaced by tests so command tests never open a desktop window.
var show = showWindow

// resumeEnv carries what the window of a new process goes on with: the
// variable the terminal front end hands over the same way.
const resumeEnv = "OURO_RESUME"

// resume is what the window of a new process goes on with: the profile it
// showed, and the session, see handOver.
type resume struct {
	Profile string `json:"profile,omitempty"`
	Session string `json:"session,omitempty"`
}

// handOver returns the plugin.Restarting that starts exe: ouro starts anew
// from the new build, and the window of the new process shows the profile and
// the session this one showed.
func handOver(exe string, r resume) error {
	b, _ := json.Marshal(r) // plain data
	return &plugin.Restarting{Exe: exe, Env: append(os.Environ(), resumeEnv+"="+string(b))}
}

// resumed reads what the process that started this one handed over, nil if it
// was started otherwise. The variable is read once: the node started later
// must not see it.
func resumed() (*resume, error) {
	v, ok := os.LookupEnv(resumeEnv)
	if !ok {
		return nil, nil
	}
	os.Unsetenv(resumeEnv)
	var r resume
	if err := json.Unmarshal([]byte(v), &r); err != nil || r.Session == "" {
		return nil, fmt.Errorf("the session to go on with: %q is none", v)
	}
	return &r, nil
}
