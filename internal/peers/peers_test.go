package peers

import (
	"context"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ouroboros/internal/a2a"
	"ouroboros/internal/discovery"
)

// finder finds the nodes it consists of.
type finder []discovery.Node

func (f finder) Find(context.Context) ([]discovery.Node, error) { return f, nil }

func TestMatch(t *testing.T) {
	nodes := []discovery.Node{{ID: "abc123", Name: "alpha"}, {ID: "abd456", Name: "beta"}, {ID: "ab", Name: "gamma"}}
	tests := []struct {
		target, want string
		ok           bool
	}{
		{"abc123", "abc123", true}, // full ID
		{"abc", "abc123", true},    // unique prefix
		{"beta", "abd456", true},   // name
		{"ab", "ab", true},         // exact ID wins over prefix matches
		{"a", "", false},           // ambiguous
		{"zzz", "", false},         // unknown
	}
	for _, tt := range tests {
		n, err := match(nodes, tt.target)
		if (err == nil) != tt.ok || n.ID != tt.want {
			t.Errorf("match(%q) = %+v, %v", tt.target, n, err)
		}
	}
}

func TestResolve(t *testing.T) {
	network := &Network{Finder: finder{{ID: "abc123", Name: "alpha"}}}
	ctx := context.Background()
	if n, err := network.Resolve(ctx, "alpha"); err != nil || n.ID != "abc123" {
		t.Fatalf("by name: %+v, %v", n, err)
	}
	// An address needs nobody to be found.
	if n, err := network.Resolve(ctx, "10.0.0.7:4000"); err != nil || n.Addr() != "10.0.0.7:4000" {
		t.Fatalf("by address: %+v, %v", n, err)
	}
	for _, target := range []string{"10.0.0.7:http", "a:b:c", "nobody"} {
		if _, err := network.Resolve(ctx, target); err == nil {
			t.Errorf("%q: want error", target)
		}
	}
}

func TestDescribe(t *testing.T) {
	card := a2a.AgentCard{Name: "alpha", Description: "  knows\nthe way "}
	srv := httptest.NewServer(a2a.NewHandler(card, nil))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	node := discovery.Node{Host: host}
	node.Port, _ = strconv.Atoi(port)

	if got := describe(context.Background(), node); got != "knows the way" {
		t.Fatalf("describe = %q", got)
	}
	srv.Close()
	if got := describe(context.Background(), node); got != "" {
		t.Fatalf("a node that does not answer has no description: %q", got)
	}
}

// A card somebody else wrote is cut to size.
func TestOneLine(t *testing.T) {
	got := oneLine(strings.Repeat("ä", maxCard+50))
	if utf8.RuneCountInString(got) != maxCard || !strings.HasSuffix(got, "ä…") {
		t.Fatalf("long card: %d characters", utf8.RuneCountInString(got))
	}
}

func TestStateAndLabel(t *testing.T) {
	for state, want := range map[State]string{NotConnected: "not connected", Requested: "connection requested", Connected: "connected"} {
		if state.String() != want {
			t.Errorf("%d = %q", state, state)
		}
	}
	if got := Label(discovery.Node{Host: "h", Port: 1}); got != "h:1" {
		t.Fatalf("a node without a name is called by its address: %q", got)
	}
}
