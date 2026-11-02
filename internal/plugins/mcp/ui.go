package mcp

import (
	"cmp"
	"context"
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
			{Name: "reconnect", Run: func(ctx context.Context) (string, error) { return m.connectAll(ctx, false) }},
			// A sign-in is the one place where the browser of the owner opens,
			// and it waits for the owner, not for a server to answer.
			{Name: "sign in", Run: func(ctx context.Context) (string, error) { return m.connectAll(ctx, true) }},
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
		how := cmp.Or(s.URL, strings.TrimSpace(s.Command+" "+strings.Join(s.Args, " ")))
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
			if err, _ := st.failure(); err != nil && st.currentStatus() != statusNeedsAuth {
				fmt.Fprintf(&b, "  %v\n", err)
			}
		}
	}
	b.WriteString("\nEsc leaves. \"sign in\" opens the page of the server's authorization server.")
	return b.String()
}

// connectAll connects every enabled server, each with the time to answer on
// its own, and says what became of them. With interactive, a server that asks
// for a sign-in may open the browser of the owner.
func (m *manager) connectAll(ctx context.Context, interactive bool) (string, error) {
	servers, err := m.configured()
	if err != nil {
		return "", err
	}
	wait := connectTimeout
	if interactive {
		wait += signInTimeout
	}
	var b strings.Builder
	for _, name := range sortedNames(servers) {
		s := servers[name]
		if s.Disabled {
			continue
		}
		st := m.server(name, s)
		st.drop()
		attempt, cancel := context.WithTimeout(ctx, wait)
		tools, err := m.connect(attempt, st, signIn{interactive: interactive})
		cancel()
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", name, err)
			continue
		}
		fmt.Fprintf(&b, "%s: %d tools\n", name, len(tools))
	}
	return strings.TrimSpace(b.String()), nil
}

// logOutAll drops what the servers sign in with.
func (m *manager) logOutAll(ctx context.Context) (string, error) {
	names, err := m.configuredNames()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, name := range names {
		err := m.forget(ctx, name)
		if st := m.state(name); st != nil {
			st.drop()
		}
		if err != nil {
			fmt.Fprintf(&b, "%s: %v\n", name, err)
			continue
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
		return errors.New("usage: ouro mcp add|remove|list|tools|login|logout|enable|disable")
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
		st, err := m.one(rest, "tools")
		if err != nil {
			return err
		}
		defer st.drop()
		tools, err := m.connect(ctx, st, signIn{})
		if err != nil {
			return err
		}
		for _, t := range tools {
			fmt.Printf("%s\t%s\n", toolName(rest[0], t.Name), firstLine(t.Description))
		}
		return nil
	case "login":
		st, err := m.one(rest, "login")
		if err != nil {
			return err
		}
		defer st.drop()
		// The owner is at the browser page now: the server may take as long as
		// it takes.
		attempt, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectTimeout+signInTimeout)
		defer cancel()
		// A node without a browser prints the address and takes the address
		// the browser ended up at; one with a browser opens it as usual.
		if _, err := m.connect(attempt, st, signIn{interactive: true, paste: m.paste}); err != nil {
			return err
		}
		fmt.Println(rest[0], statusConnected)
		return nil
	case "logout":
		if len(rest) == 0 {
			return errors.New("usage: ouro mcp logout NAME")
		}
		return m.forget(ctx, rest[0])
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

// one is the server the command names, as it is configured.
func (m *manager) one(rest []string, action string) (*server, error) {
	if len(rest) == 0 {
		return nil, fmt.Errorf("usage: ouro mcp %s NAME", action)
	}
	servers, err := m.configured()
	if err != nil {
		return nil, err
	}
	s, ok := servers[rest[0]]
	if !ok {
		return nil, fmt.Errorf("there is no MCP server called %q", rest[0])
	}
	return &server{name: rest[0], cfg: s}, nil
}

// addUsage is how a server is added.
const addUsage = "usage: ouro mcp add NAME URL [Header: Value ...]\n" +
	"       ouro mcp add NAME [-e KEY=VALUE ...] --command PROGRAM [ARG ...]"

// add is ouro mcp add NAME URL [Header: Value ...] or ouro mcp add NAME
// [-e KEY=VALUE ...] --command PROGRAM [ARG ...]. What the server is given to
// sign in with, an env value or a header, is kept in the credentials of the
// node, not in the configuration, and the configuration names it ${KEY}.
func (m *manager) add(args []string) error {
	if len(args) < 1 {
		return errors.New(addUsage)
	}
	name := args[0]
	s, secret, err := parseAdd(args[1:])
	if err != nil {
		return err
	}
	if err := s.Validate(name); err != nil {
		return err
	}
	if err := m.setServer(name, &s); err != nil {
		return err
	}
	if len(secret.Env) > 0 || len(secret.Headers) > 0 {
		m.put(name, s, data{}, secret)
	}
	fmt.Printf("%s added. Its tools are there after the next start of the node, or with: ouro mcp login %s\n", name, name)
	return nil
}

// parseAdd reads what follows the name of a server in ouro mcp add: the
// options of the server, then either its URL with headers or its program.
func parseAdd(args []string) (config.Server, stored, error) {
	s := config.Server{}
	secret := stored{Env: map[string]string{}, Headers: map[string]string{}}
	for len(args) > 0 {
		if args[0] != "-e" && args[0] != "--env" {
			break
		}
		if len(args) < 2 {
			return s, secret, errors.New(args[0] + " needs a value: write -e KEY=VALUE")
		}
		key, value, ok := strings.Cut(args[1], "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return s, secret, fmt.Errorf("%q is no env value: write it as KEY=VALUE", args[1])
		}
		secret.Env[key] = value
		args = args[2:]
	}
	if len(args) == 0 {
		return s, secret, errors.New(addUsage)
	}
	for key := range secret.Env {
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[key] = "${" + key + "}"
	}
	if args[0] == "--command" || args[0] == "-c" {
		if len(args) < 2 {
			return s, secret, errors.New("ouro mcp add: --command needs a program")
		}
		// Everything after the program belongs to the program: an argument of
		// the server that begins with -e is no option of ouro.
		s.Command, s.Args = args[1], args[2:]
		return s, secret, nil
	}
	s.URL = args[0]
	for _, header := range args[1:] {
		key, value, ok := strings.Cut(header, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return s, secret, fmt.Errorf("%q is no header: write it as Name: Value", header)
		}
		if s.Headers == nil {
			s.Headers = map[string]string{}
		}
		secret.Headers[key] = strings.TrimSpace(value)
		s.Headers[key] = "${" + key + "}"
	}
	return s, secret, nil
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
		if access, err := m.access(name); err == nil {
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
