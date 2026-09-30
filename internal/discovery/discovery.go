// Package discovery finds Ouroboros nodes on the local network.
//
// A client sends a probe to the multicast group, the broadcast address and
// localhost; every node listening on the discovery port answers with its Node.
package discovery

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
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
	return net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: Group, Port: port})
}

// Serve answers probes on conn with self until conn is closed.
func Serve(conn *net.UDPConn, self Node) error {
	reply, err := json.Marshal(self)
	if err != nil {
		return err
	}
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

	// Best effort: a target may be unreachable (e.g. no network), localhost always works.
	for _, ip := range []net.IP{Group, net.IPv4bcast, net.IPv4(127, 0, 0, 1)} {
		conn.WriteToUDP([]byte(probe), &net.UDPAddr{IP: ip, Port: port})
	}

	found := map[string]Node{}
	conn.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			break
		}
		if err != nil {
			return nil, err
		}
		var node Node
		if json.Unmarshal(buf[:n], &node) != nil || node.ID == "" {
			continue
		}
		node.Host = addr.IP.String()
		found[node.ID] = preferred(found[node.ID], node)
	}

	nodes := make([]Node, 0, len(found))
	for _, n := range found {
		nodes = append(nodes, n)
	}
	slices.SortFunc(nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	return nodes, nil
}

// preferred picks the more useful of two answers from the same node:
// a LAN address is reachable by others, a loopback address is not.
func preferred(prev, next Node) Node {
	if prev.ID == "" || net.ParseIP(prev.Host).IsLoopback() {
		return next
	}
	return prev
}
