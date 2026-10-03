package discovery

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"time"
)

// tailscaleApp is where the macOS app keeps the CLI when it is not on the PATH.
const tailscaleApp = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// TailscalePeers returns the addresses of the peers that are online in the
// tailnet of this host. Tailscale forwards neither multicast nor broadcast, so
// they have to be probed one by one. Without Tailscale the result is empty.
func TailscalePeers(ctx context.Context) []string {
	cli, err := exec.LookPath("tailscale")
	if err != nil {
		if cli, err = exec.LookPath(tailscaleApp); err != nil {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cli, "status", "--json").Output()
	if err != nil {
		return nil
	}
	return tailscalePeers(out)
}

// tailscalePeers extracts the IPv4 addresses of online peers from the output
// of "tailscale status --json".
func tailscalePeers(status []byte) []string {
	var s struct {
		Peer map[string]struct {
			TailscaleIPs []string
			Online       bool
		}
	}
	if json.Unmarshal(status, &s) != nil {
		return nil
	}
	var peers []string
	for _, p := range s.Peer {
		if !p.Online {
			continue
		}
		for _, ip := range p.TailscaleIPs {
			if net.ParseIP(ip).To4() != nil {
				peers = append(peers, ip)
			}
		}
	}
	return peers
}
