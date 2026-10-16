package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredBrowserBackends(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	b := &browser{}
	defer b.close()
	tool := b.tool()
	raw := func(endpoint, env string) json.RawMessage {
		data, _ := json.Marshal(settings{Provider: "camofox", Endpoint: endpoint, TokenEnv: env})
		return data
	}
	first, err := tool.Configure(raw(server.URL+"/one", ""))
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Configure(raw(server.URL+"/two", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.remotes.instances) != 0 {
		t.Fatal("configuration allocated backend resources")
	}
	tabs := json.RawMessage(`{"action":"tabs"}`)
	if _, err := first.Run(context.Background(), tabs); err != nil {
		t.Fatal(err)
	}
	firstInstance := b.remotes.get("", settings{Provider: "camofox", Endpoint: server.URL + "/one"})
	if _, err := second.Run(context.Background(), tabs); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(context.Background(), tabs); err != nil {
		t.Fatal(err)
	}
	if len(b.remotes.instances) != 2 || firstInstance.camo == nil {
		t.Fatal("switching configured tools destroyed a backend")
	}
	if _, err := tool.Configure(json.RawMessage(`{"provider":"camofox","endpoint":"http://one.invalid","token":"secret"}`)); err == nil {
		t.Fatal("accepted a plaintext credential in public settings")
	}
	t.Setenv("OURO_BROWSER_TEST_TOKEN", "private-value")
	secured, err := tool.Configure(raw(server.URL+"/one", "OURO_BROWSER_TEST_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secured.Run(context.Background(), tabs); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OURO_BROWSER_TEST_TOKEN", "rotated-private-value")
	if _, err := secured.Run(context.Background(), tabs); err != nil {
		t.Fatal(err)
	}
	if len(b.remotes.instances) != 4 {
		t.Fatal("credential rotation reused another account context")
	}
}

func TestBrowserBackendsCoexist(t *testing.T) {
	var registry browserBackends
	a := settings{Provider: "cdp", Endpoint: "http://one.invalid"}
	first := registry.get("profile", a)
	closed := 0
	first.proc = &process{end: func() { closed++ }}
	second := registry.get("profile", settings{Provider: "camofox", Endpoint: "http://two.invalid"})
	if first == second || first != registry.get("profile", a) || closed != 0 {
		t.Fatal("selecting another backend replaced the first resource")
	}
	otherAccount := a
	otherAccount.Token = "another-account"
	if registry.get("profile", otherAccount) == first {
		t.Fatal("different credentials share browser state")
	}
	registry.close()
	if closed != 1 {
		t.Fatalf("closed first resource %d times", closed)
	}
	registry.close()
	if closed != 1 {
		t.Fatal("registry closed a resource twice")
	}
	if registry.get("profile", a) == first {
		t.Fatal("closed resource was reused")
	}
	registry.close()
}
