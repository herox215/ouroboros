package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/safefile"
)

// A node may hold several agents, its profiles, each with an identity, a
// configuration, contacts, conversations and proposals of its own. The first
// profile is the directory of the node itself, as it was before there were
// profiles, so that nothing of it moves. Every further one is a directory
// below it, profilesDir/<ID>, named after the ID of its identity. What the
// profiles share is the process of the node with its socket, PID file and
// log, and the accesses to the LLMs with the list of their models: those are
// in the directory of the node, its Root.
const profilesDir = "profiles"

// Root is the directory of the node that d is a profile of: d itself for
// the first profile.
func (d Dir) Root() Dir {
	p := filepath.Clean(string(d))
	if parent := filepath.Dir(p); filepath.Base(parent) == profilesDir && isID(filepath.Base(p)) {
		return Dir(filepath.Dir(parent))
	}
	return d
}

// isID reports whether name is the ID of an identity, as the directory of a
// profile is named: twelve hex digits.
func isID(name string) bool {
	if len(name) != 12 {
		return false
	}
	for _, r := range name {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Profiles lists the profiles of the node: the first one, then the others in
// the order of their IDs.
func (d Dir) Profiles() ([]Dir, error) {
	root := d.Root()
	out := []Dir{root}
	entries, err := os.ReadDir(root.path(profilesDir))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries { // sorted by name
		if e.IsDir() && isID(e.Name()) {
			out = append(out, Dir(filepath.Join(root.path(profilesDir), e.Name())))
		}
	}
	return out, nil
}

// AddProfile makes a new profile called name, with an identity of its own and
// nothing else of another profile: no contacts, no conversations, no tools
// beyond the default. It talks to the LLM through the access and the model
// the first profile uses, until its owner picks others. The node has to be
// set up.
//
// The profile is written to a directory of its own first, which is renamed
// into place once it is whole: a profile that fails on the way is not there.
func (d Dir) AddProfile(name string) (Dir, error) {
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return "", err
	}
	root := d.Root()
	cfg, err := root.Config()
	if err != nil {
		return "", err
	}
	if !cfg.Complete() {
		return "", config.ErrIncomplete
	}
	unlock, err := safefile.Lock(root.path("profiles.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := root.unusedName(name, ""); err != nil {
		return "", err
	}
	id := identity.Generate()
	dir := Dir(filepath.Join(root.path(profilesDir), id.ID()))
	staged := Dir(filepath.Join(root.path(profilesDir), ".new-"+id.ID()))
	defer os.RemoveAll(string(staged))
	if err := staged.SaveConfig(config.Config{Name: name, PrivateKey: id.Private(), Access: cfg.Access, Model: cfg.Model}); err != nil {
		return "", err
	}
	if err := rename(string(staged), string(dir)); err != nil {
		return "", err
	}
	return dir, nil
}

// rename moves a profile into place; tests replace it to get in the way.
var rename = os.Rename

// RenameProfile calls the profile d name.
func (d Dir) RenameProfile(name string) error {
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return err
	}
	root := d.Root()
	unlock, err := safefile.Lock(root.path("profiles.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := root.unusedName(name, d); err != nil {
		return err
	}
	return config.Update(string(d), func(c *config.Config) error {
		c.Name = name
		return nil
	})
}

// checkName says what is wrong with name as the name of a profile.
func checkName(name string) error {
	if name == "" || strings.ContainsAny(name, "\n\r\t") {
		return errors.New("a profile needs a name of one line")
	}
	return nil
}

// unusedName fails if a profile of the node other than except is called
// name, whatever the case of its letters, or has it as its ID: a profile is
// selected by its name.
func (d Dir) unusedName(name string, except Dir) error {
	profiles, err := d.Profiles()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		c, err := p.Config()
		if err != nil {
			return err
		}
		if p != except && (strings.EqualFold(c.Name, name) || c.ID() == name) {
			return fmt.Errorf("there is a profile called %q already", name)
		}
	}
	return nil
}

// SelectProfile returns the profile of the node that target names: by its
// ID, by its name, whatever the case of its letters, or by the start of its
// ID that no other profile has.
func (d Dir) SelectProfile(target string) (Dir, error) {
	profiles, err := d.Profiles()
	if err != nil {
		return "", err
	}
	var found []Dir
	for _, p := range profiles {
		c, err := p.Config()
		if err != nil {
			return "", err
		}
		if target == c.ID() || strings.EqualFold(target, c.Name) {
			return p, nil
		}
		if target != "" && strings.HasPrefix(c.ID(), target) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("the node has no profile %q: ouro profile list lists them", target)
	}
	return "", fmt.Errorf("%q starts the ID of several profiles: name one of them by more of it", target)
}

// Workspace is the directory the agent of the profile works in: its
// commands start there. It is outside the directory of the node, which no
// tool for files changes, next to it: one for every profile, named after
// its ID, however many the node has.
func (d Dir) Workspace() (string, error) {
	id, err := d.Identity()
	if err != nil {
		return "", err
	}
	return filepath.Join(string(d.Root())+"-workspaces", id.ID()), nil
}

// WorkingDir is the Workspace of d, or "" while d has no key to name it
// after: a node that is not set up yet gets none here, and its agent works
// from the home directory of the user.
func (d Dir) WorkingDir() string {
	if cfg, err := d.Config(); err != nil || cfg.PrivateKey == "" {
		return ""
	}
	workspace, _ := d.Workspace()
	return workspace
}
