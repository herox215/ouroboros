package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"ouroboros/internal/home"
	"ouroboros/internal/node"
	nodeplugin "ouroboros/internal/plugins/node"
	tuiplugin "ouroboros/internal/plugins/tui"
)

// A node may hold several agents, its profiles (see home.Dir.Profiles). Each
// of them is an app of its own here, with all the plugins its configuration
// turns on: they share nothing but the frame, the process of the node and
// the accesses to the LLMs. The node of the first one runs the others, and
// the UI of each shows them all.

// profile is what runs one profile of a node, as its plugins hand it over
// once they are started.
type profile struct {
	node *nodeplugin.Profile
	ui   *tuiplugin.Profile
}

// newProfiles returns the app of the node of s, with the app of every
// profile of it. While there are several, each has its workspace.
func newProfiles(s Settings, t Terminal) (*App, error) {
	dirs, err := home.Dir(s.Home).Profiles()
	if err != nil {
		return nil, err
	}
	apps := map[home.Dir]*App{}
	nodes := make([]*nodeplugin.Profile, len(dirs))
	uis := make([]*tuiplugin.Profile, len(dirs))
	for i, dir := range dirs {
		if len(dirs) > 1 {
			workspace, err := dir.Workspace()
			if err != nil {
				return nil, fmt.Errorf("the profile in %s: %w", dir, err)
			}
			if err := os.MkdirAll(workspace, 0o700); err != nil {
				return nil, err
			}
		}
		at := s
		at.Home = string(dir)
		var p profile
		app, err := NewWith(standard(at, t, &p)...)
		if err != nil {
			if i > 0 {
				err = fmt.Errorf("the profile in %s: %w", dir, err)
			}
			return nil, err
		}
		apps[dir], nodes[i], uis[i] = app, p.node, p.ui
	}
	for _, n := range nodes[1:] {
		nodes[0].Join(n)
	}
	if !slices.Contains(uis, nil) { // no profile turned the UI off
		tuiplugin.Together(uis)
	}
	first := apps[dirs[0]]
	first.profiles, first.home = apps, dirs[0]
	return first, nil
}

// standard returns the plugins of a profile, see plugins; tests replace it
// to get in the way.
var standard = plugins

// forProfile returns the app that runs the command args, and the command
// without "--profile PROFILE", which may come first: the app of the profile
// it names, by its ID, its name or the start of its ID (see
// home.Dir.SelectProfile).
//
// The node itself runs from the first profile, whichever is named. While
// there are several, every command that is about one of them needs it to be
// named: a script that names none fails, rather than run for one it did not
// mean. The UI shows them all, and opens on the one named, or on the first.
func (a *App) forProfile(args []string) (*App, []string, error) {
	target, args, err := profileFlag(args)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case a.profiles == nil && target != "":
		return nil, nil, errors.New("this ouro runs no profiles")
	case a.profiles == nil, target == "" && (len(a.profiles) == 1 || nodeWide(args)):
		return a, args, nil
	case target == "":
		return nil, nil, fmt.Errorf("the node has several profiles: name the one to run it for, as in ouro --profile PROFILE %s (ouro profile lists them)",
			strings.Join(args, " "))
	}
	dir, err := a.home.SelectProfile(target)
	if err != nil {
		return nil, nil, err
	}
	app, ok := a.profiles[dir]
	switch {
	case !ok:
		return nil, nil, fmt.Errorf("the profile %q was added after this ouro started: run it again", target)
	case len(args) > 0 && args[0] == node.ServeCommand:
		return a, args, nil
	}
	return app, args, nil
}

// profileFlag returns the profile "--profile PROFILE" or
// "--profile=PROFILE" names at the start of args, if it is there, and the
// rest of args.
func profileFlag(args []string) (string, []string, error) {
	var target string
	switch {
	case len(args) > 0 && strings.HasPrefix(args[0], "--profile="):
		target, args = strings.TrimPrefix(args[0], "--profile="), args[1:]
	case len(args) > 1 && args[0] == "--profile":
		target, args = args[1], args[2:]
	case len(args) == 0 || args[0] != "--profile":
		return "", args, nil
	}
	if strings.TrimSpace(target) == "" {
		return "", nil, errors.New("--profile needs the ID or the name of a profile (ouro profile lists them)")
	}
	return target, args, nil
}

// nodeWide reports whether the command args is about the node, not about
// one of its profiles: the UI, which shows them all, too.
func nodeWide(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "start":
		return len(args) == 1 // with --setup it sets up a profile
	case "stop", node.ServeCommand, "update", "list", "profile", "tui", "help", "-h", "--help":
		return true
	}
	return false
}
