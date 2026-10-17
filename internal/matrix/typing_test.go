package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
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

func TestTypingToUnverifiableRoom(t *testing.T) {
	h := newHomeserver(t)
	h.breakRoom("!broken:test", true)
	h.room("!crowded:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipJoin})
	c := testClient(t, h.server.URL)
	for _, room := range []string{"!broken:test", "!crowded:test"} {
		if err := c.TypingTo(context.Background(), room, "@alice:test", true); err == nil {
			t.Fatalf("typing in %s", room)
		}
	}
	if _, _, typing := h.requests(); len(typing) != 0 {
		t.Fatalf("typing notifications %v", typing)
	}
}
