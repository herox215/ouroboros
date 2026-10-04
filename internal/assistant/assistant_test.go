package assistant

import (
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

func names(tools []agent.Tool) string {
	var s []string
	for _, t := range tools {
		s = append(s, t.Name)
	}
	return strings.Join(s, ",")
}

func TestPick(t *testing.T) {
	all := PeerTools(nil, func() string { return "abc123" })
	tests := []struct {
		allowed []string
		want    string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{config.AllTools}, "list_peers,ask_peer"},
		{[]string{"ask_peer"}, "ask_peer"},
		{[]string{"ask_peer", "list_peers"}, "list_peers,ask_peer"},
	}
	for _, tt := range tests {
		got, err := pick(all, tt.allowed)
		if err != nil || names(got) != tt.want {
			t.Errorf("pick(%v) = %q, %v", tt.allowed, names(got), err)
		}
	}
	// A typo must not silently take a tool away.
	if _, err := pick(all, []string{"ask_peers"}); err == nil || !strings.Contains(err.Error(), `"ask_peers"`) {
		t.Fatalf("unknown tool: %v", err)
	}
}

func TestUnknownToolFailsEarly(t *testing.T) {
	cfg := config.Config{BaseURL: "http://x", Model: "m", Name: "n"}
	b := Builder{Tools: func() []agent.Tool { return nil }}
	cfg.Tools = &config.Tools{A2A: []string{"nope"}}
	if _, err := b.ForPeers(cfg); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("ForPeers: %v", err)
	}
	cfg.Tools = &config.Tools{H2A: []string{"nope"}}
	a := New(func() (config.Config, error) { return cfg, nil }, b)
	if err := a.Ready(); err == nil || !strings.Contains(err.Error(), "tools.h2a") {
		t.Fatalf("Ready: %v", err)
	}
}
