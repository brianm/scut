package main

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

// nodeMap is immutable after publication. Self names the DNS server's own node,
// so zone authority records need no separately configured nameserver or alias.
type nodeMap struct {
	Self  string
	Hosts map[string][]netip.Addr
}

// nodeSource returns a complete view of the nodes visible to local tailscaled.
// A failed read must return an error, not an empty view that deletes old names.
type nodeSource interface {
	Nodes(context.Context) (*nodeMap, error)
}

type localSource struct{ client local.Client }

func (s *localSource) Nodes(ctx context.Context) (*nodeMap, error) {
	status, err := s.client.Status(ctx)
	if err != nil {
		return nil, err
	}
	return nodesFromStatus(status)
}

func nodesFromStatus(status *ipnstate.Status) (*nodeMap, error) {
	if status == nil || status.BackendState != "Running" || status.Self == nil {
		return nil, fmt.Errorf("tailscaled is not running with a local node")
	}
	nodes := &nodeMap{Hosts: make(map[string][]netip.Addr)}
	add := func(peer *ipnstate.PeerStatus) (string, error) {
		// DNSName is Tailscale's assigned name; HostName can be invalid or
		// duplicated and is deliberately not treated as an alias.
		name, _, _ := strings.Cut(strings.TrimSuffix(strings.ToLower(peer.DNSName), "."), ".")
		if !validLabel(name) {
			return "", fmt.Errorf("invalid Tailscale DNS name %q", peer.DNSName)
		}
		if _, exists := nodes.Hosts[name]; exists {
			return "", fmt.Errorf("duplicate Tailscale DNS label %q", name)
		}
		nodes.Hosts[name] = append([]netip.Addr(nil), peer.TailscaleIPs...)
		return name, nil
	}
	var err error
	if nodes.Self, err = add(status.Self); err != nil {
		return nil, err
	}
	for _, peer := range status.Peer {
		// Status also includes transport bookkeeping for former peers and
		// nodes belonging to people we shared with. Neither is our inventory.
		if peer == nil || !peer.InNetworkMap || peer.ShareeNode {
			continue
		}
		if _, err := add(peer); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

func validLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
