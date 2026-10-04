// Package discovery finds Ouroboros nodes on the local network.
//
// A client sends a probe to the multicast group, the broadcast addresses and
// localhost; every node listening on the discovery port answers with its Node,
// both to the sender and to the multicast group. The group answer arrives on
// the fixed discovery port, so a firewall only needs to allow that port.
//
// Networks that forward neither multicast nor broadcast, such as VPNs, are
// covered by probing known peers directly.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ouroboros/internal/identity"
)

const probe = "OURO?"

// Group is the multicast group nodes join.
var Group = net.IPv4(239, 255, 41, 38)

// Node is what a node announces about itself.
type Node struct {
	// ID is the fingerprint of PublicKey (identity.IDOf). An answer whose ID
	// does not fit its key is dropped, so no node can pass for another.
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
	Port  int    `json:"port"`
	// PublicKey is the base64 Ed25519 key that signs this node's requests.
	PublicKey string `json:"public_key,omitempty"`
	Host      string `json:"host,omitempty"` // filled in by the discovering side
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

// PeerSource names hosts to probe directly, as "host" or "host:port" for
// another discovery port: the way to nodes that no probe to the network reaches.
//
// To add a source (another VPN, a list from a file): write a func like
// TailscalePeers and contribute it from a plugin with plugin.Add, like the
// tailscale plugin in internal/plugins/tailscale does. It must not block for
// long and returns nil if it has nothing.
type PeerSource func(ctx context.Context) []string

// Scanner finds the nodes that are online: it broadcasts a probe and also
// sends it to every host its sources name.
type Scanner struct {
	Port int           // discovery port
	Wait time.Duration // how long answers are collected
	// Sources returns the sources to ask, read anew for every scan. May be nil.
	Sources func() []PeerSource
}

// Find returns the nodes on the network and on the hosts named by the sources.
func (s *Scanner) Find(ctx context.Context) ([]Node, error) {
	var peers []string
	if s.Sources != nil {
		for _, source := range s.Sources() {
			peers = append(peers, source(ctx)...)
		}
	}
	return Discover(ctx, s.Port, s.Wait, peers...)
}

// Discover probes the network and collects answers for the given duration, or
// until ctx is done. Peers are hosts to probe directly, as "host" or
// "host:port" for another discovery port. Nodes are returned sorted by name,
// one entry per ID.
func Discover(ctx context.Context, port int, wait time.Duration, peers ...string) ([]Node, error) {
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
	for _, ip := range append(broadcasts(), Group, net.IPv4bcast, net.IPv4(127, 0, 0, 1)) {
		conn.WriteToUDP([]byte(probe), &net.UDPAddr{IP: ip, Port: port})
	}
	for _, peer := range peers {
		// Resolving a name may take a while; a probe that is too late is dropped.
		go func() {
			if addr, err := net.ResolveUDPAddr("udp4", peerAddr(peer, port)); err == nil {
				conn.WriteToUDP([]byte(probe), addr)
			}
		}()
	}

	deadline := time.Now().Add(wait)
	for _, c := range conns {
		c.SetReadDeadline(deadline)
	}
	// A caller that gives up ends the collecting early.
	defer context.AfterFunc(ctx, func() { expire(conns) })()

	found, err := gather(conns)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	nodes := make([]Node, 0, len(found))
	for _, n := range found {
		nodes = append(nodes, n)
	}
	slices.SortFunc(nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return nodes, nil
}

// peerAddr adds the discovery port to a peer given without one.
func peerAddr(peer string, port int) string {
	if _, _, err := net.SplitHostPort(peer); err == nil {
		return peer
	}
	return net.JoinHostPort(strings.Trim(peer, "[]"), strconv.Itoa(port))
}

// broadcasts returns the broadcast address of every network this host is on.
// The limited broadcast only leaves through the default interface, which
// misses further networks such as a second LAN or a layer 2 VPN.
func broadcasts() []net.IP {
	ifaces, _ := net.Interfaces()
	var ips []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || len(ipnet.Mask) != net.IPv4len {
				continue
			}
			ip := ipnet.IP.To4()
			bcast := make(net.IP, net.IPv4len)
			for i := range bcast {
				bcast[i] = ip[i] | ^ipnet.Mask[i]
			}
			ips = append(ips, bcast)
		}
	}
	return ips
}

// gather collects the answers arriving on conns until their read deadlines,
// one per ID. It returns only when every collector has finished, so none is
// left behind sending to a channel nobody reads.
func gather(conns []*net.UDPConn) (map[string]Node, error) {
	answers := make(chan Node)
	errs := make(chan error, len(conns))
	for _, c := range conns {
		go func() { errs <- collect(c, answers) }()
	}

	found := map[string]Node{}
	var failed error
	for pending := len(conns); pending > 0; {
		select {
		case node := <-answers:
			found[node.ID] = preferred(found[node.ID], node)
		case err := <-errs:
			if err != nil && failed == nil {
				failed = err
				expire(conns) // nothing is worth waiting for any more
			}
			pending--
		}
	}
	return found, failed
}

// expire ends the reading on conns: a deadline in the past is over at once.
func expire(conns []*net.UDPConn) {
	for _, c := range conns {
		c.SetReadDeadline(time.Now())
	}
}

// collect sends every node answering on conn to answers until the read
// deadline of conn. Somebody has to receive from answers until it returns.
// Answers that are not a node, or that claim an ID their key does not give
// them, are dropped.
func collect(conn *net.UDPConn, answers chan<- Node) error {
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
		if json.Unmarshal(buf[:n], &node) != nil || node.ID == "" || node.ID != identity.IDOf(node.PublicKey) {
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
