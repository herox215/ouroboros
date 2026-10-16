package browser

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRemoteEndpointKeepsQuery(t *testing.T) {
	endpoint := "wss://browser.test/devtools/browser/id?session=abc/"
	s, err := (settings{Provider: "cdp", Endpoint: endpoint}).normalized()
	if err != nil || s.Endpoint != endpoint {
		t.Fatalf("endpoint changed: %q, %v", s.Endpoint, err)
	}
}

func TestRemoteContextMustBeCreated(t *testing.T) {
	t.Setenv("OURO_REMOTE_CONTEXT_TEST_TOKEN", "test-secret")
	f := newFake(t)
	f.on("Target.createBrowserContext", func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	server := wsBrowser(t, f)
	b := newBrowser(f)
	defer b.close()
	_, err := call(context.Background(), b, settings{Provider: "cdp", Endpoint: server.URL, TokenEnv: "OURO_REMOTE_CONTEXT_TEST_TOKEN"}, `{"action":"open","url":"https://example.com"}`)
	if err == nil || !strings.Contains(err.Error(), "isolated context") {
		t.Fatalf("missing context ID: %v", err)
	}
	if slices.Contains(f.got(), "Target.createTarget") {
		t.Fatal("created a page in the hosted default context")
	}
}
