package discovery

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

var errTest = errors.New("test error")

// replace sets the hook to with for the rest of the test.
func replace[T any](t *testing.T, hook *T, with T) {
	t.Helper()
	old := *hook
	*hook = with
	t.Cleanup(func() { *hook = old })
}

// noPort is a port no socket can be bound to.
const noPort = 1 << 16

// The discovery socket hears what this host sends to the group: without that
// a prober on the same host would miss the group answers.
func TestListenEnablesMulticastLoopback(t *testing.T) {
	conn, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	loop := -1
	raw.Control(func(fd uintptr) {
		loop, err = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP)
	})
	if err != nil || loop != 1 {
		t.Fatalf("IP_MULTICAST_LOOP = %d, %v", loop, err)
	}
}

func TestListenFails(t *testing.T) {
	if conn, err := Listen(noPort); err == nil || conn != nil {
		t.Fatalf("a port that does not exist: %v, %v", conn, err)
	}

	// A socket that cannot be set up is not handed out.
	var level, opt, value int
	replace(t, &setsockoptInt, func(_, l, o, v int) error {
		level, opt, value = l, o, v
		return errTest
	})
	conn, err := Listen(0)
	if !errors.Is(err, errTest) || conn != nil {
		t.Fatalf("without multicast loopback: %v, %v", conn, err)
	}
	if level != syscall.IPPROTO_IP || opt != syscall.IP_MULTICAST_LOOP || value != 1 {
		t.Fatalf("setsockopt(%d, %d, %d)", level, opt, value)
	}
}

func TestListenGroupFails(t *testing.T) {
	if conn, err := listenGroup(noPort); err == nil || conn != nil {
		t.Fatalf("a port that does not exist: %v, %v", conn, err)
	}

	t.Run("no socket", func(t *testing.T) {
		replace(t, &syscallSocket, func(domain, typ, proto int) (int, error) {
			if domain != syscall.AF_INET || typ != syscall.SOCK_DGRAM {
				t.Errorf("socket(%d, %d, %d)", domain, typ, proto)
			}
			return -1, errTest
		})
		if conn, err := listenGroup(0); !errors.Is(err, errTest) || conn != nil {
			t.Fatalf("got %v, %v", conn, err)
		}
	})
	t.Run("no connection", func(t *testing.T) {
		replace(t, &netFilePacketConn, func(*os.File) (net.PacketConn, error) { return nil, errTest })
		if conn, err := listenGroup(0); !errors.Is(err, errTest) || conn != nil {
			t.Fatalf("got %v, %v", conn, err)
		}
	})
}

// A socket that fails for another reason than being closed ends the serving
// with that reason.
func TestServeReportsFailedRead(t *testing.T) {
	conn, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(-time.Second))
	if err := Serve(conn, func() Node { return Node{ID: "x"} }); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestDiscoverFails(t *testing.T) {
	t.Run("no socket", func(t *testing.T) {
		replace(t, &netListenUDP, func(string, *net.UDPAddr) (*net.UDPConn, error) { return nil, errTest })
		if nodes, err := Discover(context.Background(), serve(t, 0, node("alpha")), wait); !errors.Is(err, errTest) || nodes != nil {
			t.Fatalf("got %+v, %v", nodes, err)
		}
	})
	// A socket that stops working is not waited for, and what was found until
	// then is not passed off as everybody.
	t.Run("broken socket", func(t *testing.T) {
		replace(t, &netListenUDP, func(network string, addr *net.UDPAddr) (*net.UDPConn, error) {
			conn, err := net.ListenUDP(network, addr)
			if err == nil {
				conn.Close()
			}
			return conn, err
		})
		start := time.Now()
		nodes, err := Discover(context.Background(), serve(t, 0, node("alpha")), time.Minute)
		if !errors.Is(err, net.ErrClosed) || nodes != nil || time.Since(start) > 5*time.Second {
			t.Fatalf("got %+v, %v after %v", nodes, err, time.Since(start))
		}
	})
}

// Nodes are listed by name, in whatever order they answer.
func TestDiscoverSortsByName(t *testing.T) {
	var port int
	var peers []string
	for _, name := range []string{"delta", "alpha", "echo", "charlie", "bravo"} {
		port = serve(t, 0, node(name))
		peers = append(peers, "127.0.0.1:"+strconv.Itoa(port))
	}
	nodes, err := Discover(context.Background(), port, wait, peers...)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.Name)
	}
	if want := []string{"alpha", "bravo", "charlie", "delta", "echo"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
