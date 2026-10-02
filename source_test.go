package main

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestNodesFromStatus(t *testing.T) {
	status := &ipnstate.Status{
		BackendState: "Running",
		Self:         &ipnstate.PeerStatus{DNSName: "SERVER.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {DNSName: "Freki.tail.ts.net.", HostName: "wrong-name", InNetworkMap: true, Online: false, TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}},
			key.NewNode().Public(): {DNSName: "old.tail.ts.net.", InNetworkMap: false},
			key.NewNode().Public(): {DNSName: "sharee.tail.ts.net.", InNetworkMap: true, ShareeNode: true},
		},
	}
	nodes, err := nodesFromStatus(status)
	if err != nil {
		t.Fatal(err)
	}
	if nodes.Self != "server" || len(nodes.Hosts) != 2 || nodes.Hosts["freki"][0].String() != "100.64.0.2" {
		t.Fatalf("incorrect mapping: %+v", nodes)
	}
	// The cache must not inherit mutable slices from the provider.
	status.Self.TailscaleIPs[0] = netip.MustParseAddr("100.64.0.9")
	if nodes.Hosts["server"][0].String() != "100.64.0.1" {
		t.Fatal("source address slice was not copied")
	}
	status.Peer[key.NewNode().Public()] = &ipnstate.PeerStatus{DNSName: "freki.other.ts.net.", InNetworkMap: true}
	if _, err := nodesFromStatus(status); err == nil {
		t.Fatal("ambiguous short label accepted")
	}
	status.BackendState = "Stopped"
	if _, err := nodesFromStatus(status); err == nil {
		t.Fatal("stopped daemon accepted as valid inventory")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLocalAPI(t *testing.T) {
	source := &localSource{client: local.Client{
		OmitAuth: true,
		Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.Path != "/localapi/v0/status" || r.URL.RawQuery != "" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"BackendState":"Running","Self":{"DNSName":"server.tail.ts.net.","TailscaleIPs":["100.64.0.1"]}}`))}, nil
		}),
	}}
	if nodes, err := source.Nodes(context.Background()); err != nil || nodes.Self != "server" {
		t.Fatalf("local API decoding failed: nodes=%v err=%v", nodes, err)
	}
}
