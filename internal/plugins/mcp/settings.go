package mcp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ouroboros/internal/config"
	"ouroboros/plugin"
)

// settings is the servers as the settings screen shows them: every configured
// server, how it is, and the tools it offers with whether the owner gives each
// to the agents.
func (m *manager) settings(ctx context.Context) []plugin.MCPServer {
	servers, err := m.configured()
	if err != nil {
		return nil
	}
	out := make([]plugin.MCPServer, 0, len(servers))
	for _, name := range sortedNames(servers) {
		s := servers[name]
		srv := plugin.MCPServer{Name: name, Disabled: s.Disabled, Status: statusDisabled,
			How: cmp.Or(s.URL, strings.TrimSpace(s.Command+" "+strings.Join(s.Args, " ")))}
		if !s.Disabled {
			srv.Status = "not connected"
			if st := m.state(name); st != nil {
				st.mu.Lock()
				srv.Status, srv.Tools = st.status, make([]plugin.MCPTool, 0, len(st.tools))
				for _, t := range st.tools {
					srv.Tools = append(srv.Tools, plugin.MCPTool{
						Name:        t.Name,
						Description: firstLine(t.Description),
						On:          wanted(s, t.Name),
					})
				}
				st.mu.Unlock()
			}
		}
		out = append(out, srv)
	}
	return out
}

// wanted says whether the configuration gives the tool name of the server s to
// the agents: a server without a list gives all of them.
func wanted(s config.Server, name string) bool {
	return len(s.Tools) == 0 || slices.Contains(s.Tools, name)
}

// setTool turns one tool of a server on or off, in the configuration and for
// the agents at once: a tool that is off is no longer in their list, and one
// that comes back is, from the next message on.
func (m *manager) setTool(ctx context.Context, server, tool string, on bool) error {
	servers, err := m.configured()
	if err != nil {
		return err
	}
	s, ok := servers[server]
	if !ok {
		return fmt.Errorf("there is no MCP server called %q", server)
	}
	st := m.state(server)
	var listed []*mcpsdk.Tool
	if st != nil {
		st.mu.Lock()
		listed = slices.Clone(st.tools)
		st.mu.Unlock()
	}
	if len(listed) == 0 {
		return fmt.Errorf("the MCP server %s is not connected, so it lists no tools: connect it, or start ouro anew", server)
	}
	// The list of the configuration is the tools the owner wants, and a
	// server without one wants all: what the server lists now is the base,
	// with this tool as the change says.
	next := make([]string, 0, len(listed))
	for _, t := range listed {
		switch {
		case t.Name == tool:
			if on {
				next = append(next, t.Name)
			}
		case wanted(s, t.Name):
			next = append(next, t.Name)
		}
	}
	if len(next) == 0 {
		// An empty list of the configuration means every tool of the server:
		// the last one that stays cannot be turned off.
		return fmt.Errorf("%s is the only tool of %s, which cannot be off: turn the server off instead", tool, server)
	}
	if on && !slices.Contains(next, tool) {
		return fmt.Errorf("the MCP server %s lists no tool called %q", server, tool)
	}
	if err := config.Update(string(m.dir), func(c *config.Config) error {
		cfg, ok := c.MCPServers()[server]
		if !ok {
			return fmt.Errorf("there is no MCP server called %q", server)
		}
		cfg.Tools = next
		c.MCP.Servers[server] = cfg
		return nil
	}); err != nil {
		return err
	}
	if st != nil {
		st.cfg.Tools = next
		m.registerTools(st, listed)
	}
	return nil
}

// setEnabled turns a whole server on or off: off drops its connection and
// takes its tools back at once, on connects it again, whose tools come with
// the connection.
func (m *manager) setEnabled(ctx context.Context, server string, on bool) error {
	if err := m.setDisabled(server, !on); err != nil {
		return err
	}
	st := m.state(server)
	if st == nil { // a process that did not connect the servers, a front end without a node
		return nil
	}
	st.cfg.Disabled = !on
	if !on {
		st.drop()
		m.unregister(st)
		return nil
	}
	st.set("not connected", nil)
	// The owner waits for no server here: the screen asks again, and says
	// what became of it then.
	go func() {
		attempt, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		_, _ = m.connect(attempt, st, false)
	}()
	return nil
}

// service is what a front end reads of the servers, see plugin.MCP.
func (m *manager) service() plugin.MCP {
	return plugin.MCP{Servers: m.settings, SetTool: m.setTool, SetServer: m.setEnabled}
}
