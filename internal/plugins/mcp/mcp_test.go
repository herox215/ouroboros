package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
)

// TestConnectAndCall talks to a server of the SDK: it starts the connection,
// reads its tools, calls one, and calls it again on the same connection.
func TestConnectAndCall(t *testing.T) {
	fake := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "1"}, nil)
	mcpsdk.AddTool(fake, &mcpsdk.Tool{Name: "echo", Description: "echo text"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + in.Text}}}, nil, nil
		})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return fake }, nil))
	defer httpServer.Close()

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]bool{}}
	st := &server{name: "fake", cfg: config.Server{URL: httpServer.URL}}
	defer st.drop()

	tools, err := m.connect(context.Background(), st)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" || tools[0].Description != "echo text" {
		t.Fatalf("tools = %v", tools)
	}
	for _, tt := range []struct{ args, want string }{
		{`{"text":"hi"}`, "echo: hi"},
		{`{"text":"again"}`, "echo: again"},
	} {
		out, err := m.call(context.Background(), st, "echo", json.RawMessage(tt.args))
		if err != nil {
			t.Fatalf("call %s: %v", tt.args, err)
		}
		if out != tt.want {
			t.Fatalf("call %s = %q, want %q", tt.args, out, tt.want)
		}
	}
	// A tool the server does not have is an error, not a panic.
	if _, err := m.call(context.Background(), st, "missing", nil); err == nil {
		t.Fatal("a missing tool was called")
	}
}

// TestToolName keeps the names of the tools in what providers accept.
func TestToolName(t *testing.T) {
	for _, tt := range []struct{ server, tool, want string }{
		{"atlassian", "getJiraIssue", "atlassian__getJiraIssue"},
		{"my server", "read file", "my_server__read_file"},
		{"jira", "search", "jira__search"},
	} {
		if got := toolName(tt.server, tt.tool); got != tt.want {
			t.Errorf("toolName(%q, %q) = %q, want %q", tt.server, tt.tool, got, tt.want)
		}
	}
	if got := toolName("s", string(make([]byte, 100))); len(got) > 64 {
		t.Errorf("a long name is %d bytes long", len(got))
	}
}
