package gui

import (
	"fmt"
	"ouroboros/plugin"
)

// MCPServers lists the MCP servers of the selected profile: how each is
// reached and how it is, and the tools it offers with whether the agents get
// each. It connects none, so a server that is down stays down for a page that
// reads it again after every switch.
func (w *Window) MCPServers(profile int) ([]plugin.MCPServer, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return nil, err
	}
	out := []plugin.MCPServer{}
	if p.ctx == nil {
		return out, nil
	}
	for _, m := range plugin.All[plugin.MCP](p.ctx) {
		if m.Servers != nil {
			out = append(out, m.Servers(w.ctx)...)
		}
	}
	return out, nil
}

// MCPSetServer turns a whole MCP server of the selected profile on or off: its
// tools are gone while it is off, and nothing starts for it.
func (w *Window) MCPSetServer(profile int, server string, on bool) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	if p.ctx != nil {
		for _, m := range plugin.All[plugin.MCP](p.ctx) {
			if m.SetServer != nil {
				return m.SetServer(w.ctx, server, on)
			}
		}
	}
	return fmt.Errorf("MCP server %q is not available", server)
}

// MCPLogin signs the selected profile's node in to the server: the page of
// its authorization server opens in the browser, and the window says that it
// waits for the answer. It returns only with the answer, which can take
// minutes: a front end keeps the window usable meanwhile.
func (w *Window) MCPLogin(profile int, server string) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	if p.ctx != nil {
		for _, m := range plugin.All[plugin.MCP](p.ctx) {
			if m.SignIn != nil {
				return m.SignIn(w.ctx, server)
			}
		}
	}
	return fmt.Errorf("MCP server %q is not available", server)
}

// MCPLogout drops what the server signs in with: the next call needs a sign-in
// again.
func (w *Window) MCPLogout(profile int, server string) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	if p.ctx != nil {
		for _, m := range plugin.All[plugin.MCP](p.ctx) {
			if m.Logout != nil {
				return m.Logout(w.ctx, server)
			}
		}
	}
	return fmt.Errorf("MCP server %q is not available", server)
}

// MCPSetTool turns one tool of an MCP server on or off, so that the agents of
// the selected profile get it or not. It holds from the next message on,
// without a start of the node.
func (w *Window) MCPSetTool(profile int, server, tool string, on bool) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	if p.ctx != nil {
		for _, m := range plugin.All[plugin.MCP](p.ctx) {
			if m.SetTool != nil {
				return m.SetTool(w.ctx, server, tool, on)
			}
		}
	}
	return fmt.Errorf("MCP server %q is not available", server)
}
