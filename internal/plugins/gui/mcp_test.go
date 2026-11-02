package gui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

// The MCP methods of the window read and change the servers of the selected
// profile only, and read them without connecting one.
func TestWindowMCP(t *testing.T) {
	w, _ := modelWindow(t)
	var changed [2][]plugin.MCPChange
	read := [2]int{}
	for i := range 2 {
		k := kernel.New()
		if err := k.Start(plugin.New("mcp", func(ctx *plugin.Context, _ struct{}) error {
			plugin.Add(ctx, plugin.MCP{
				Servers: func(context.Context) []plugin.MCPServer {
					read[i]++
					return []plugin.MCPServer{{Name: "fixture", How: "https://example.invalid/mcp", Status: "connected",
						Tools: []plugin.MCPTool{{Name: "search", Description: "Search tickets", On: true}}}}
				},
				SetServer: func(_ context.Context, server string, on bool) error {
					changed[i] = append(changed[i], plugin.MCPChange{Server: server, On: on})
					return nil
				},
				SetTool: func(_ context.Context, server, tool string, on bool) error {
					changed[i] = append(changed[i], plugin.MCPChange{Server: server, Tool: tool, On: on})
					return nil
				},
			})
			return nil
		})); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k.Stop)
		w.profiles[i].ctx = k.Context()
	}
	servers, err := w.MCPServers(1)
	if err != nil || len(servers) != 1 || servers[0].Name != "fixture" || len(servers[0].Tools) != 1 || !servers[0].Tools[0].On {
		t.Fatalf("%+v %v", servers, err)
	}
	if read[0] != 0 || read[1] != 1 {
		t.Fatalf("read %v", read)
	}
	if err := w.MCPSetServer(1, "fixture", false); err != nil {
		t.Fatal(err)
	}
	if err := w.MCPSetTool(1, "fixture", "search", false); err != nil {
		t.Fatal(err)
	}
	if len(changed[0]) != 0 || len(changed[1]) != 2 {
		t.Fatalf("changes %+v", changed)
	}
	if c := changed[1][0]; c.Server != "fixture" || c.Tool != "" || c.On {
		t.Fatalf("server change %+v", c)
	}
	if c := changed[1][1]; c.Server != "fixture" || c.Tool != "search" || c.On {
		t.Fatalf("tool change %+v", c)
	}
}

// A profile without plugins or without the MCP service has no servers and
// changes none; an index the window has no profile for is refused, and a
// cancelled window reads nothing.
func TestWindowMCPUnavailable(t *testing.T) {
	w, _ := modelWindow(t)
	w.profiles[0].ctx = nil
	if got, err := w.MCPServers(0); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("without plugins: %#v %v", got, err)
	}
	if err := w.MCPSetServer(0, "fixture", true); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("SetServer without plugins: %v", err)
	}
	if err := w.MCPSetTool(0, "fixture", "search", true); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("SetTool without plugins: %v", err)
	}
	k := kernel.New()
	if err := k.Start(plugin.New("mcp", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.MCP{})
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	w.profiles[1].ctx = k.Context()
	if got, err := w.MCPServers(1); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("without a service: %#v %v", got, err)
	}
	if err := w.MCPSetServer(1, "fixture", true); err == nil {
		t.Fatal("ungiven SetServer accepted")
	}
	if err := w.MCPSetTool(1, "fixture", "search", true); err == nil {
		t.Fatal("ungiven SetTool accepted")
	}
	for _, profile := range []int{-1, 2} {
		if _, err := w.MCPServers(profile); err == nil || !strings.Contains(err.Error(), "invalid GUI profile index") {
			t.Fatalf("MCPServers(%d): %v", profile, err)
		}
		if err := w.MCPSetServer(profile, "fixture", true); err == nil || !strings.Contains(err.Error(), "invalid GUI profile index") {
			t.Fatalf("MCPSetServer(%d): %v", profile, err)
		}
		if err := w.MCPSetTool(profile, "fixture", "search", true); err == nil || !strings.Contains(err.Error(), "invalid GUI profile index") {
			t.Fatalf("MCPSetTool(%d): %v", profile, err)
		}
	}
	w.cancel()
	if _, err := w.MCPServers(1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
