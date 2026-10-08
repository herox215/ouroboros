package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// profile lists the profiles of the node, adds one or renames one.
//
// A profile is added while the node is stopped: the node that runs knows the
// profiles it started with, and keeps their secrets from its agents, so a
// new one starts with the node the next time.
func (c *nodeCommands) profile(_ context.Context, args []string) error {
	root := c.local.Home.Root()
	switch {
	case len(args) == 0:
		profiles, err := root.Profiles()
		if err != nil {
			return err
		}
		for _, p := range profiles {
			cfg, err := p.Config()
			if err != nil {
				return err
			}
			id, err := p.Identity() // a node from before nodes had keys gets one
			if err != nil {
				return err
			}
			fmt.Fprintf(c.term.Out, "%s  %s  model=%s\n", id.ID(), cfg.Name, cfg.Model)
		}
		return nil
	case args[0] == "add" && len(args) == 2:
		if _, running := c.local.Running(); running {
			return errors.New("the node runs: stop it first (ouro stop), and the new profile starts with it the next time")
		}
		p, err := root.AddProfile(args[1])
		if err != nil {
			return err
		}
		// The directory of a profile is named after its ID.
		fmt.Fprintf(c.term.Out, "+ profile %s (%s), with the access and the model of the first one\n", filepath.Base(string(p)), strings.TrimSpace(args[1]))
		return nil
	case args[0] == "rename" && len(args) == 3:
		p, err := root.SelectProfile(args[1])
		if err != nil {
			return err
		}
		return p.RenameProfile(args[2])
	}
	return errors.New("usage: ouro profile [add NAME | rename PROFILE NAME]")
}
