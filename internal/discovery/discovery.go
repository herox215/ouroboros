// Package discovery finds Ouroboros nodes on the local network.
//
// A client sends a probe to the multicast group, the broadcast address and
// localhost; every node listening on the discovery port answers with its Node,
// both to the sender and to the multicast group. The group answer arrives on
// the fixed discovery port, so a firewall only needs to allow that port.
package discovery

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const probe = "OURO?"

// Group is the multicast group nodes join.
var Group = net.IPv4(239, 255, 41, 38)

// Node is what a node announces about itself.
type Node struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Port  int    `json:"port"`
	Host  string `json:"host,omitempty"` // filled in by the discovering side
}

// Addr returns the node's A2A address as host:port.
func (n Node) Addr() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(n.Port))
}

// Listen opens the discovery socket. Several nodes on one host may share the port.
func Listen(port int) (*net.UDPConn, error) {
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: Group, Port: port})
	if err != nil {
		return nil, err
	}
	// Go disables multicast loopback; enable it so probers on this host hear group answers.
	raw, err := conn.SyscallConn()
	if err == nil {
		raw.Control(func(fd uintptr) {
			err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
		})
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// listenGroup opens a socket that only receives datagrams sent to the group.
// Unlike Listen it binds the group address instead of the wildcard address,
// so it cannot steal unicast probes from a node on the same port.
func listenGroup(port int) (*net.UDPConn, error) {
	// Built by hand: net.ListenPacket rebinds multicast addresses to the wildcard.
	// SOCK_CLOEXEC is Linux-only; set close-on-exec under ForkLock like the net package does on BSD/macOS.
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "discovery")
	defer f.Close()
	mreq := &syscall.IPMreq{Multiaddr: [4]byte(Group.To4())}
	if err := errors.Join(
		syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1),
		syscall.Bind(fd, &syscall.SockaddrInet4{Port: port, Addr: mreq.Multiaddr}),
		syscall.SetsockoptIPMreq(fd, syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq),
	); err != nil {
		return nil, err
	}
	conn, err := net.FilePacketConn(f)
	if err != nil {
		return nil, err
	}
	return conn.(*net.UDPConn), nil
}

// Serve answers probes on conn with self until conn is closed.
func Serve(conn *net.UDPConn, self Node) error {
	reply, err := json.Marshal(self)
	if err != nil {
		return err
	}
	group := &net.UDPAddr{IP: Group, Port: conn.LocalAddr().(*net.UDPAddr).Port}
	buf := make([]byte, len(probe)+1)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		if string(buf[:n]) == probe {
			conn.WriteToUDP(reply, addr)
			conn.WriteToUDP(reply, group)
		}
	}
}

// Discover probes the network and collects answers for the given duration.
// Nodes are returned sorted by name, one entry per ID.
func Discover(port int, wait time.Duration) ([]Node, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Answers sent to the group get through firewalls that block the ephemeral port.
	conns := []*net.UDPConn{conn}
	if group, err := listenGroup(port); err == nil {
		defer group.Close()
		conns = append(conns, group)
	}

	// Best effort: a target may be unreachable (e.g. no network), localhost always works.
	for _, ip := range []net.IP{Group, net.IPv4bcast, net.IPv4(127, 0, 0, 1)} {
		conn.WriteToUDP([]byte(probe), &net.UDPAddr{IP: ip, Port: port})
	}

	answers := make(chan Node)
	errs := make(chan error, len(conns))
	deadline := time.Now().Add(wait)
	for _, c := range conns {
		go func() { errs <- collect(c, deadline, answers) }()
	}

	found := map[string]Node{}
	for pending := len(conns); pending > 0; {
		select {
		case node := <-answers:
			found[node.ID] = preferred(found[node.ID], node)
		case err := <-errs:
			if err != nil {
				return nil, err
			}
			pending--
		}
	}

	nodes := make([]Node, 0, len(found))
	for _, n := range found {
		nodes = append(nodes, n)
	}
	slices.SortFunc(nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return nodes, nil
}

// collect sends every node answering on conn to answers until deadline.
func collect(conn *net.UDPConn, deadline time.Time, answers chan<- Node) error {
	conn.SetReadDeadline(deadline)
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil
		}
		if err != nil {
			return err
		}
		var node Node
		if json.Unmarshal(buf[:n], &node) != nil || node.ID == "" {
			continue
		}
		node.Host = addr.IP.String()
		answers <- node
	}
}

// preferred picks the more useful of two answers from the same node:
// a LAN address is reachable by others, a loopback address is not.
func preferred(prev, next Node) Node {
	if prev.ID == "" || net.ParseIP(prev.Host).IsLoopback() {
		return next
	}
	return prev
}
