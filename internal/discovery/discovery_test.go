package discovery

import (
	"net"
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
	nodes, err := Discover(port, wait)
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
	nodes, err := Discover(port, wait)
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
