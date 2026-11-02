package plugin

import "context"

// MCP is what the MCP servers of the node are to a front end: the servers with
// how they are and what they offer, and what the owner decides about them.
// The plugin internal/plugins/mcp contributes it, and a build without that
// plugin has none; whoever shows it takes what the settings screen shows.
type MCP struct {
	// Servers lists the configured servers with their state and their tools.
	// It connects none: a server that is down is not started for a screen
	// that reads it every few seconds.
	Servers func(ctx context.Context) []MCPServer
	// SetTool turns one tool of a server on or off, so that the agents get
	// it or not. It holds from the next message on, without a start of the
	// node.
	SetTool func(ctx context.Context, server, tool string, on bool) error
	// SetServer turns a whole server on or off: its tools are gone while it
	// is off, and nothing starts for it.
	SetServer func(ctx context.Context, server string, on bool) error
}

// MCPServer is one MCP server as a front end shows it.
type MCPServer struct {
	Name string
	// How is how it is reached: its URL, or the program it starts.
	How string
	// Disabled says that the owner turned it off.
	Disabled bool
	// Status is how it is: connected, needs authentication, disabled, not
	// connected, or why it failed.
	Status string
	// Tools are the tools it offers, with whether the owner gives each to the
	// agents. A server that is not connected has none.
	Tools []MCPTool
}

// MCPTool is one tool of an MCP server.
type MCPTool struct {
	Name        string
	Description string
	On          bool // the owner gives it to the agents
}

// MCPChange is one change the owner makes on the MCP servers: a whole server
// on or off, or one tool of it.
type MCPChange struct {
	Server string
	Tool   string // the tool of the server, "" for the whole server
	On     bool
}
