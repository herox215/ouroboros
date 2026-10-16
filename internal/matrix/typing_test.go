package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTypingTo(t *testing.T) {
	var states []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/state") {
			json.NewEncoder(w).Encode(safeEvents())
			return
		}
		if r.Method != "PUT" || r.URL.Path != "/_matrix/client/v3/rooms/!room:test/typing/@bot:test" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		var body struct {
			Typing  bool
			Timeout int
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Typing && body.Timeout != 30000 {
			t.Errorf("timeout: %d", body.Timeout)
		}
		states = append(states, body.Typing)
		w.Write([]byte("{}"))
	}))
	defer server.Close()
	c := testClient(t, server.URL)
	for _, active := range []bool{true, false} {
		if err := c.TypingTo(context.Background(), "!room:test", "@alice:test", active); err != nil {
			t.Fatal(err)
		}
	}
	for _, peer := range []string{"", "@other:test"} {
		if err := c.TypingTo(context.Background(), "!room:test", peer, true); err == nil {
			t.Fatal("accepted wrong peer")
		}
	}
	if len(states) != 2 || !states[0] || states[1] {
		t.Fatalf("typing requests: %v", states)
	}
}
