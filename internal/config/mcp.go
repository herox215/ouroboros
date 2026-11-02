package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
)

// MCP holds the MCP servers the agents of the node may use, by the name they
// are called with there. Their tools become tools of the agent, and the owner
// manages them with /mcp in a front end or with ouro mcp.
//
// What a server needs to sign in to it, an env value or a header, is no
// secret in the configuration: the plugin that connects the servers keeps it
// in credentials.json, as an access of the provider mcp:NAME, and the
// configuration only names the variable, like "Authorization": "${ATLASSIAN}".
type MCP struct {
	Servers map[string]Server `json:"servers,omitempty"`
}

// Server is one MCP server: a program that is started for it, or a URL it is
// reached at.
type Server struct {
	// URL is the endpoint of a remote server, Streamable HTTP, like
	// https://mcp.atlassian.com/v1/mcp.
	URL string `json:"url,omitempty"`
	// Command is the program of a local server, spoken to over its standard
	// input and output, and Args what it is started with.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env and Headers are what the server is given on the way: environment
	// variables for a program, headers for a URL. A value of the form
	// ${NAME} is the secret of the node's credentials for this server, or,
	// without one there, the variable NAME of the environment ouro runs in.
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// ClientID and ClientSecret are the OAuth client of a remote server that
	// is registered with its authorization server already, instead of
	// registering itself: a server that does not offer dynamic client
	// registration. The secret may be ${NAME}, read like a value of Env and
	// Headers; without one the client is a public client.
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	// Disabled says that the server is not connected: its tools are not
	// there, and nothing starts for it.
	Disabled bool `json:"disabled,omitempty"`
	// Tools are the tools of the server the agent gets, by the name the
	// server gives them. Empty means all of them.
	Tools []string `json:"tools,omitempty"`
}

// MCPServers are the MCP servers of the node, by name.
func (c Config) MCPServers() map[string]Server {
	if c.MCP == nil {
		return nil
	}
	return c.MCP.Servers
}

// MCPServerNames returns the names of the MCP servers, sorted.
func (c Config) MCPServerNames() []string {
	servers := c.MCPServers()
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// serverName is what a server may be called: a name that is also a part of
// the name of every tool of the server, which providers accept.
var serverName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

// loopback reports whether host is this machine.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Validate reports what keeps the server from being connected, by its name.
func (s Server) Validate(name string) error {
	if !serverName.MatchString(name) {
		return fmt.Errorf("mcp: %q is no name of a server: start with a letter, then letters, digits, _ or -, at most 32", name)
	}
	if s.ClientSecret != "" && s.ClientID == "" {
		return fmt.Errorf("mcp: the server %s has a client secret without a client id", name)
	}
	switch {
	case s.URL != "" && s.Command != "":
		return fmt.Errorf("mcp: the server %s has both a url and a command: it is one or the other", name)
	case s.URL == "" && s.Command == "":
		return fmt.Errorf("mcp: the server %s has neither a url nor a command", name)
	case s.Command != "":
		if len(s.Headers) > 0 {
			return fmt.Errorf("mcp: the server %s is a program, which takes no headers: set env instead", name)
		}
		if s.ClientID != "" || s.ClientSecret != "" {
			return fmt.Errorf("mcp: the server %s is a program, which has no OAuth client: the fields are for a remote server", name)
		}
		return nil
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("mcp: %s: %q is no URL", name, s.URL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())) {
		return fmt.Errorf("mcp: the URL of %s has to be https, or http to this machine", name)
	}
	if len(s.Args) > 0 || len(s.Env) > 0 {
		return fmt.Errorf("mcp: the server %s is reached at a URL, which takes no args and no env: set headers instead", name)
	}
	return nil
}
