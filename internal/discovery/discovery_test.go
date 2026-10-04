package discovery

import (
	"context"
	"net"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const wait = 300 * time.Millisecond

func serve(t *testing.T, port int, self Node) int {
	t.Helper()
	conn, err := Listen(port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go Serve(conn, self)
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func TestDiscoverFindsNode(t *testing.T) {
	port := serve(t, 0, Node{ID: "abc123", Name: "alpha", Model: "gpt", Port: 41337})
	nodes, err := Discover(context.Background(), port, wait)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("want 1 node, got %+v", nodes)
	}
	n := nodes[0]
	if n.ID != "abc123" || n.Name != "alpha" || n.Model != "gpt" || n.Port != 41337 || n.Host == "" {
		t.Fatalf("got %+v", n)
	}
}

func TestDiscoverNothing(t *testing.T) {
	conn, _ := net.ListenUDP("udp4", nil) // reserve a port nobody answers on
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	nodes, err := Discover(context.Background(), port, wait)
	if err != nil || len(nodes) != 0 {
		t.Fatalf("got %+v, %v", nodes, err)
	}
}

func TestServeIgnoresOtherPackets(t *testing.T) {
	port := serve(t, 0, Node{ID: "abc123"})
	conn, _ := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	defer conn.Close()
	conn.Write([]byte("hello"))
	conn.SetReadDeadline(time.Now().Add(wait))
	if n, err := conn.Read(make([]byte, 64)); err == nil {
		t.Fatalf("unexpected reply of %d bytes", n)
	}
}

func TestServeStopsOnClose(t *testing.T) {
	conn, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- Serve(conn, Node{ID: "x"}) }()
	conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestPreferredChoosesLANOverLoopback(t *testing.T) {
	lo := Node{ID: "a", Host: "127.0.0.1"}
	lan := Node{ID: "a", Host: "192.168.1.5"}
	if got := preferred(Node{}, lo); got != lo {
		t.Fatalf("first answer should win, got %+v", got)
	}
	if got := preferred(lo, lan); got != lan {
		t.Fatalf("LAN should replace loopback, got %+v", got)
	}
	if got := preferred(lan, lo); got != lan {
		t.Fatalf("loopback must not replace LAN, got %+v", got)
	}
}

func TestAddr(t *testing.T) {
	if got := (Node{Host: "10.0.0.1", Port: 80}).Addr(); got != "10.0.0.1:80" {
		t.Fatalf("got %q", got)
	}
}

// A firewall may drop the unicast answer to the prober's ephemeral port;
// the answer to the group on the discovery port must still arrive.
func TestServeAnswersToGroup(t *testing.T) {
	port := serve(t, 0, Node{ID: "abc123"})
	group, err := listenGroup(port)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	requireMulticast(t, group)
	conn, _ := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	defer conn.Close()
	conn.Write([]byte(probe))
	group.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 4096)
	n, err := group.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), `"abc123"`) {
		t.Fatalf("got %q, %v", buf[:n], err)
	}
}

// requireMulticast skips the test if this host drops multicast sent to itself,
// e.g. because a firewall blocks incoming traffic on the port.
func requireMulticast(t *testing.T, group *net.UDPConn) {
	t.Helper()
	conn, _ := net.ListenUDP("udp4", nil)
	defer conn.Close()
	conn.WriteToUDP([]byte("ping"), group.LocalAddr().(*net.UDPAddr))
	group.SetReadDeadline(time.Now().Add(wait))
	if _, err := group.Read(make([]byte, 16)); err != nil {
		t.Skip("multicast is blocked on this host (firewall?)")
	}
}

func TestDiscoverProbesPeers(t *testing.T) {
	port := serve(t, 0, Node{ID: "abc123", Name: "alpha"})
	conn, _ := net.ListenUDP("udp4", nil) // a discovery port nobody answers on
	other := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()

	nodes, err := Discover(context.Background(), other, wait, "localhost:"+strconv.Itoa(port), "no-such-host.invalid")
	if err != nil || len(nodes) != 1 || nodes[0].ID != "abc123" {
		t.Fatalf("got %+v, %v", nodes, err)
	}
}

func TestPeerAddr(t *testing.T) {
	for peer, want := range map[string]string{
		"10.0.0.7":      "10.0.0.7:41338",
		"box.example":   "box.example:41338",
		"10.0.0.7:5000": "10.0.0.7:5000",
	} {
		if got := peerAddr(peer, 41338); got != want {
			t.Errorf("peerAddr(%q) = %q, want %q", peer, got, want)
		}
	}
}

func TestBroadcasts(t *testing.T) {
	for _, ip := range broadcasts() {
		if ip.To4() == nil || ip.IsLoopback() {
			t.Fatalf("unexpected broadcast address %v", ip)
		}
	}
}

func TestTailscalePeers(t *testing.T) {
	status := `{"Self": {"TailscaleIPs": ["100.64.0.1"], "Online": true}, "Peer": {
		"a": {"TailscaleIPs": ["100.64.0.2", "fd7a:115c:a1e0::2"], "Online": true},
		"b": {"TailscaleIPs": ["100.64.0.3"], "Online": false}}}`
	if got := tailscalePeers([]byte(status)); !slices.Equal(got, []string{"100.64.0.2"}) {
		t.Fatalf("got %v", got)
	}
	if got := tailscalePeers([]byte("not json")); got != nil {
		t.Fatalf("got %v", got)
	}
}

// A caller that gives up does not have to wait for the answers.
func TestDiscoverStopsWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := Discover(ctx, serve(t, 0, Node{ID: "abc123"}), time.Minute); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

// A socket that fails ends the discovery, and the collector of the other
// socket ends with it: it must neither keep reading nor stay stuck with an
// answer nobody takes.
func TestGatherLeavesNoCollectorBehind(t *testing.T) {
	broken, _ := net.ListenUDP("udp4", nil)
	broken.Close() // reading from it fails at once
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busy.SetReadDeadline(time.Now().Add(time.Minute))

	// Answers keep coming, so the collector has one to hand over when the other fails.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				busy.WriteToUDP([]byte(`{"id":"abc123"}`), busy.LocalAddr().(*net.UDPAddr))
			}
		}
	}()

	start := time.Now()
	if _, err := gather([]*net.UDPConn{broken, busy}); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	stacks := make([]byte, 1<<20)
	stacks = stacks[:runtime.Stack(stacks, true)]
	if strings.Contains(string(stacks), "discovery.collect(") {
		t.Fatalf("a collector is still running:\n%s", stacks)
	}
}
