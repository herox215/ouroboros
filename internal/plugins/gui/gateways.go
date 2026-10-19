package gui

import (
	"fmt"
	"ouroboros/plugin"
)

// Gateways reads dynamic public contributions for exactly the selected profile.
func (w *Window) Gateways(profile int) ([]plugin.GatewaySettings, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return nil, err
	}
	out := []plugin.GatewaySettings{}
	if p.ctx == nil {
		return out, nil
	}
	for _, g := range plugin.All[plugin.Gateway](p.ctx) {
		if g.Settings == nil {
			continue
		}
		s, err := g.Settings(w.ctx)
		if err != nil {
			return nil, err
		}
		s.Name, s.Title = g.Name, g.Title
		out = append(out, s)
	}
	return out, nil
}

// SaveGateway sends credentials only to the selected profile's contribution.
// Credentials are never part of a window snapshot or returned settings.
func (w *Window) SaveGateway(profile int, name string, change plugin.GatewayChange) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	if p.ctx != nil {
		for _, g := range plugin.All[plugin.Gateway](p.ctx) {
			if g.Name == name && g.Save != nil {
				return g.Save(w.ctx, change)
			}
		}
	}
	return fmt.Errorf("gateway %q is not available", name)
}
