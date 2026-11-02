package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/plugin"
)

// screen is /mcp in the terminal UI: the servers with how they are, and what
// the owner does about them.
func (m *manager) screen() plugin.Screen {
	return plugin.Screen{
		Command: "mcp",
		Title:   "MCP servers",
		Body: func(ctx context.Context) (string, error) {
			servers, err := m.configured()
			if err != nil {
				return "", err
			}
			return m.status(servers), nil
		},
		Actions: []plugin.Action{
			{Name: "reconnect", Run: m.reconnectAll},
			{Name: "sign in", Run: m.signInAll},
			{Name: "log out", Run: m.logOutAll},
		},
	}
}

// status lists the servers, without connecting one: the screen asks again
// every few seconds, and a server that is down must not be started for it.
func (m *manager) status(servers map[string]config.Server) string {
	if len(servers) == 0 {
		return "No MCP server is configured. Add one with:\n  ouro mcp add NAME URL   or   ouro mcp add NAME --command PROGRAM"
	}
	var b strings.Builder
	for _, name := range sortedNames(servers) {
		s := servers[name]
		how := cmp.Or(s.URL, s.Command+" "+strings.Join(s.Args, " "))
		status, tools := statusDisabled, 0
		if !s.Disabled {
			status = "not connected"
			if st := m.state(name); st != nil {
				status, tools = st.snapshot()
			}
		}
		fmt.Fprintf(&b, "%-16s %-46s %s", name, how, status)
		if tools > 0 {
			fmt.Fprintf(&b, ", %d tools", tools)
		}
		b.WriteString("\n")
		if st := m.state(name); st != nil {
			if err, _ := st.failure(); err != nil && !strings.HasPrefix(st.currentStatus(), statusNeedsAuth) {
				fmt.Fprintf(&b, "  %v\n", err)
			}
		}
	}
	b.WriteString("\nEsc leaves. The agent uses a server only when it is connected.")
	return b.String()
}

// reconnectAll connects every enabled server again, with its tools.
func (m *manager) reconnectAll(ctx context.Context) (string, error) {
	servers, err := m.configured()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, name := range sortedNames(servers) {
		s := servers[name]
		if s.Disabled {
			continue
		}
		st := &server{name: name, cfg: s}
		m.mu.Lock()
		if old, ok := m.servers[name]; ok {
			st = old
			st.cfg = s
		}
		m.servers[name] = st
		m.mu.Unlock()
		st.drop()
		tools, err := m.connect(ctx, st)
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(&b, "%s: %d tools\n", name, len(tools))
	}
	return strings.TrimSpace(b.String()), nil
}

// signInAll connects the servers that asked for a sign-in, which opens the
// page of the authorization server.
func (m *manager) signInAll(ctx context.Context) (string, error) {
	return m.reconnectAll(ctx)
}

// logOutAll drops what the servers sign in with.
func (m *manager) logOutAll(ctx context.Context) (string, error) {
	names, err := m.configuredNames()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, name := range names {
		if err := m.forget(name); err != nil {
			fmt.Fprintf(&b, "%s: %v\n", name, err)
			continue
		}
		if st := m.state(name); st != nil {
			st.drop()
		}
		fmt.Fprintf(&b, "%s: logged out\n", name)
	}
	return strings.TrimSpace(b.String()), nil
}

func (m *manager) configuredNames() ([]string, error) {
	servers, err := m.configured()
	if err != nil {
		return nil, err
	}
	return sortedNames(servers), nil
}

// command is ouro mcp, for a node without a front end.
func (m *manager) command() plugin.Command {
	return plugin.Command{
		Name: "mcp", Args: "add|remove|list|tools|login|logout|enable|disable NAME",
		Help: "manage the MCP servers of the node",
		Run:  m.runCommand,
	}
}

func (m *manager) runCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ouro mcp " + "add|remove|list|tools|login|logout|enable|disable")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "list":
		servers, err := m.configured()
		if err != nil {
			return err
		}
		fmt.Println(m.status(servers))
		return nil
	case "tools":
		if len(rest) == 0 {
			return errors.New("usage: ouro mcp tools NAME")
		}
		servers, err := m.configured()
		if err != nil {
			return err
		}
		s, ok := servers[rest[0]]
		if !ok {
			return fmt.Errorf("there is no MCP server called %q", rest[0])
		}
		st := &server{name: rest[0], cfg: s}
		tools, err := m.connect(ctx, st)
		if err != nil {
			return err
		}
		for _, t := range tools {
			fmt.Printf("%s\t%s\n", toolName(rest[0], t.Name), firstLine(t.Description))
		}
		return nil
	case "login":
		if len(rest) == 0 {
			return errors.New("usage: ouro mcp login NAME")
		}
		servers, err := m.configured()
		if err != nil {
			return err
		}
		s, ok := servers[rest[0]]
		if !ok {
			return fmt.Errorf("there is no MCP server called %q", rest[0])
		}
		st := &server{name: rest[0], cfg: s}
		if _, err := m.connect(ctx, st); err != nil {
			return err
		}
		fmt.Println(rest[0], statusConnected)
		return nil
	case "logout":
		if len(rest) == 0 {
			return errors.New("usage: ouro mcp logout NAME")
		}
		return m.forget(rest[0])
	case "add":
		return m.add(rest)
	case "remove":
		if len(rest) == 0 {
			return errors.New("usage: ouro mcp remove NAME")
		}
		return m.setServer(rest[0], nil)
	case "enable", "disable":
		if len(rest) == 0 {
			return fmt.Errorf("usage: ouro mcp %s NAME", action)
		}
		return m.setDisabled(rest[0], action == "disable")
	}
	return fmt.Errorf("unknown command %q: ouro mcp add|remove|list|tools|login|logout|enable|disable", action)
}

// add is ouro mcp add NAME URL [Header: Value ...] or ouro mcp add NAME
// --command PROGRAM [ARG ...] [-e KEY=VALUE ...]. What it is given to sign in
// with, a header or an env value, is kept in the credentials of the node, not
// in the configuration.
func (m *manager) add(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: ouro mcp add NAME URL [Header: Value ...] | ouro mcp add NAME --command PROGRAM [ARG ...] [-e KEY=VALUE ...]")
	}
	name := args[0]
	s := config.Server{}
	env, headers := map[string]string{}, map[string]string{}
	var configEnv, configHeaders = map[string]string{}, map[string]string{}
	rest := args[1:]
	if len(rest) > 0 && (rest[0] == "--command" || rest[0] == "-c") {
		if len(rest) < 2 {
			return errors.New("ouro mcp add: --command needs a program")
		}
		s.Command = rest[1]
		s.Args = rest[2:]
		for i := 0; i < len(s.Args); i++ {
			if key, value, ok := strings.Cut(s.Args[i], "="); ok && key == "-e" {
				env[value] = ""
				configEnv[value] = "${" + value + "}"
				s.Args = append(s.Args[:i], s.Args[i+1:]...)
				i--
			}
		}
	} else {
		s.URL = rest[0]
		for _, header := range rest[1:] {
			key, value, ok := strings.Cut(header, ":")
			if !ok {
				return fmt.Errorf("%q is no header: write it as Name: Value", header)
			}
			key = strings.TrimSpace(key)
			headers[key] = strings.TrimSpace(value)
			configHeaders[key] = "${" + key + "}"
		}
	}
	if err := s.Validate(name); err != nil {
		return err
	}
	s.Env, s.Headers = configEnv, configHeaders
	if err := m.setServer(name, &s); err != nil {
		return err
	}
	if len(env) > 0 || len(headers) > 0 {
		m.put(name, s, data{}, stored{Env: env, Headers: headers})
	}
	fmt.Printf("%s added. Its tools are there after the next start of the node, or with: ouro mcp login %s\n", name, name)
	return nil
}

// setServer writes the server name, or removes it with none.
func (m *manager) setServer(name string, s *config.Server) error {
	if err := config.Update(string(m.dir), func(c *config.Config) error {
		if s == nil {
			if _, ok := c.MCPServers()[name]; !ok {
				return fmt.Errorf("there is no MCP server called %q", name)
			}
			delete(c.MCP.Servers, name)
			return nil
		}
		if err := s.Validate(name); err != nil {
			return err
		}
		if c.MCP == nil {
			c.MCP = &config.MCP{Servers: map[string]config.Server{}}
		}
		if c.MCP.Servers == nil {
			c.MCP.Servers = map[string]config.Server{}
		}
		c.MCP.Servers[name] = *s
		return nil
	}); err != nil {
		return err
	}
	if s == nil {
		access, err := m.access(name)
		if err == nil {
			_ = credentials.Open(string(m.dir)).Remove(access.ID)
		}
	}
	return nil
}

// setDisabled turns a server on or off in the configuration.
func (m *manager) setDisabled(name string, disabled bool) error {
	return config.Update(string(m.dir), func(c *config.Config) error {
		s, ok := c.MCPServers()[name]
		if !ok {
			return fmt.Errorf("there is no MCP server called %q", name)
		}
		s.Disabled = disabled
		c.MCP.Servers[name] = s
		return nil
	})
}

// snapshot is how the server is, and how many tools it has.
func (st *server) snapshot() (string, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.status, len(st.tools)
}

func (st *server) failure() (error, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.err, st.connected
}

func (st *server) currentStatus() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.status
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// unusedJSON keeps the import of encoding/json for the data of a server, in
// one place.
var _ = json.Marshal
