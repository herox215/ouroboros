package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
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

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	st := &server{name: "fake", cfg: config.Server{URL: httpServer.URL}}
	defer st.drop()

	tools, err := m.connect(context.Background(), st, false)
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

// TestParseAdd keeps the server and its secrets apart: -e takes its value as
// an argument of its own, and an argument of the program that begins with -e
// stays an argument.
func TestParseAdd(t *testing.T) {
	for _, tt := range []struct {
		name       string
		args       []string
		want       config.Server
		wantEnv    map[string]string
		wantHeader map[string]string
		wantErr    bool
	}{{
		name: "program with arguments",
		args: []string{"--command", "npx", "-y", "@modelcontextprotocol/server-filesystem", "."},
		want: config.Server{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "."}},
	}, {
		name:    "env of the program",
		args:    []string{"-e", "TOKEN=abc", "-e", "HOST=x=y", "--command", "server", "--port", "1"},
		want:    config.Server{Command: "server", Args: []string{"--port", "1"}, Env: map[string]string{"TOKEN": "${TOKEN}", "HOST": "${HOST}"}},
		wantEnv: map[string]string{"TOKEN": "abc", "HOST": "x=y"},
	}, {
		name: "an argument of the program is no option",
		args: []string{"--command", "server", "-e=x", "-e", "y=z"},
		want: config.Server{Command: "server", Args: []string{"-e=x", "-e", "y=z"}},
	}, {
		name:       "a URL with a header",
		args:       []string{"https://mcp.atlassian.com/v1/mcp", "Authorization: Bearer abc"},
		want:       config.Server{URL: "https://mcp.atlassian.com/v1/mcp", Headers: map[string]string{"Authorization": "${Authorization}"}},
		wantHeader: map[string]string{"Authorization": "Bearer abc"},
	}, {
		name:    "an env value without =",
		args:    []string{"-e", "TOKEN", "--command", "server"},
		wantErr: true,
	}, {
		name:    "nothing to add",
		args:    []string{"-e", "TOKEN=abc"},
		wantErr: true,
	}} {
		t.Run(tt.name, func(t *testing.T) {
			s, secret, err := parseAdd(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseAdd(%q) = %+v, want an error", tt.args, s)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAdd(%q): %v", tt.args, err)
			}
			if !reflect.DeepEqual(s, tt.want) {
				t.Fatalf("server = %+v, want %+v", s, tt.want)
			}
			if !reflect.DeepEqual(emptyNil(secret.Env), emptyNil(tt.wantEnv)) {
				t.Fatalf("env secrets = %v, want %v", secret.Env, tt.wantEnv)
			}
			if !reflect.DeepEqual(emptyNil(secret.Headers), emptyNil(tt.wantHeader)) {
				t.Fatalf("header secrets = %v, want %v", secret.Headers, tt.wantHeader)
			}
		})
	}
}

func emptyNil(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// TestToolNameUnique keeps two tools of the same name apart after the names
// were cleaned and shortened.
func TestToolNameUnique(t *testing.T) {
	taken := map[string]string{}
	for _, tt := range []struct{ server, tool string }{
		{"read", "file.x"},
		{"read", "file_x"},
		{"a", "_b"},
		{"a_", "b"},
		{"s", strings.Repeat("x", 80)},
		{"s", strings.Repeat("x", 81)},
	} {
		name := toolName(tt.server, tt.tool)
		owner := tt.server + "/" + tt.tool
		if had, ok := taken[name]; ok {
			name = uniqToolName(tt.server, tt.tool, taken)
			if had == owner || taken[name] != "" {
				t.Fatalf("%s/%s got the name %q of %q", tt.server, tt.tool, name, had)
			}
		}
		taken[name] = owner
		if len(name) > 64 {
			t.Fatalf("%s/%s is %d bytes long", tt.server, tt.tool, len(name))
		}
	}
	// file.x and file_x come out as the same name before cleaning: one of
	// them gets a hash.
	one, two := toolName("read", "file.x"), toolName("read", "file_x")
	if one != two {
		t.Fatalf("%q and %q were expected to collide", one, two)
	}
	if uniqToolName("read", "file_x", map[string]string{one: "read/file.x"}) == one {
		t.Fatal("the second tool kept the name of the first")
	}
}

// TestCallTwiceAfterALostSession calls a tool on a session that is gone: the
// call fails, and says that it does not know whether the tool ran. The next
// call connects again and works, and the server ran the tool once, not twice.
func TestCallTwiceAfterALostSession(t *testing.T) {
	var calls atomic.Int32
	fake := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake", Version: "1"}, nil)
	mcpsdk.AddTool(fake, &mcpsdk.Tool{Name: "write", Description: "write a ticket"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcpsdk.CallToolResult, any, error) {
			calls.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "written"}}}, nil, nil
		})
	httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return fake }, nil))
	defer httpServer.Close()

	dir := home.Dir(t.TempDir())
	// The call that connects again reads the configuration: it is where a
	// disabled or removed server is noticed.
	if err := config.Update(string(dir), func(c *config.Config) error {
		c.MCP = &config.MCP{Servers: map[string]config.Server{"fake": {URL: httpServer.URL}}}
		return nil
	}); err != nil {
		t.Fatalf("config: %v", err)
	}
	m := &manager{dir: dir, servers: map[string]*server{}, added: map[string]string{}}
	st := &server{name: "fake", cfg: config.Server{URL: httpServer.URL}}
	defer st.drop()
	if _, err := m.connect(context.Background(), st, false); err != nil {
		t.Fatalf("connect: %v", err)
	}
	st.mu.Lock()
	dead := st.session
	st.mu.Unlock()
	dead.Close()

	_, err := m.call(context.Background(), st, "write", json.RawMessage(`{"text":"one"}`))
	if err == nil {
		t.Fatal("a call on a closed session worked")
	}
	if !strings.Contains(err.Error(), "not known whether the tool ran") {
		t.Fatalf("error = %v, want one that says the outcome is unknown", err)
	}
	out, err := m.call(context.Background(), st, "write", json.RawMessage(`{"text":"two"}`))
	if err != nil {
		t.Fatalf("the call after the lost session: %v", err)
	}
	if out != "written" {
		t.Fatalf("out = %q", out)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("the server ran the tool %d times, want 1: the failed call was repeated", got)
	}
}
