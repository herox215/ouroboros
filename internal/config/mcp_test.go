package config

import (
	"strings"
	"testing"
)

// TestMCPValidateClient: a pre-registered OAuth client belongs to a remote
// server, and its secret is no secret without the client it belongs to.
func TestMCPValidateClient(t *testing.T) {
	for _, tt := range []struct {
		name   string
		server Server
		want   string // what the error has to say, "" for no error
	}{
		{"a URL with a client and its secret", Server{URL: "https://example.com/mcp", ClientID: "id", ClientSecret: "secret"}, ""},
		{"a public client", Server{URL: "https://example.com/mcp", ClientID: "id"}, ""},
		{"a secret without an id", Server{URL: "https://example.com/mcp", ClientSecret: "secret"}, "client secret without a client id"},
		{"a program with a client", Server{Command: "server", ClientID: "id", ClientSecret: "secret"}, "has no OAuth client"},
		{"a program with a client id alone", Server{Command: "server", ClientID: "id"}, "has no OAuth client"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.server.Validate("jira")
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want one that says %q", err, tt.want)
			}
		})
	}
}
