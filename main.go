package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"tailscale.com/client/local"
)

func main() {
	zone := flag.String("zone", "", "authoritative DNS zone (required)")
	listen := flag.String("listen", ":53", "UDP and TCP listen address")
	socket := flag.String("tailscale-socket", "", "tailscaled socket (default: Tailscale's platform default)")
	interval := flag.Duration("refresh-interval", 5*time.Minute, "interval between Tailscale refreshes")
	ttl := flag.Duration("dns-ttl", 5*time.Minute, "positive DNS response TTL")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()
	if *debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if *zone != "" {
		*zone = strings.ToLower(dns.Fqdn(*zone))
	}
	if err := validateConfig(*zone, *interval, *ttl); err != nil || flag.NArg() != 0 {
		if err == nil {
			err = errors.New("unexpected positional arguments")
		}
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cache := newCache(&localSource{client: local.Client{Socket: *socket, UseSocketOnly: *socket != ""}})
	slog.Info("starting scut", "zone", *zone, "listen", *listen, "refresh_interval", *interval, "dns_ttl", *ttl, "tailscale_socket", *socket)
	_ = cache.refresh(ctx) // A temporary source failure must not prevent startup.
	go cache.run(ctx, *interval)
	handler := &zoneHandler{ctx: ctx, zone: *zone, ttl: uint32(*ttl / time.Second), cache: cache}
	if err := serve(ctx, *listen, handler); err != nil {
		slog.Error("DNS server failed", "error", err)
		os.Exit(1)
	}
}

func validateConfig(zone string, interval, ttl time.Duration) error {
	if zone == "" {
		return errors.New("zone is required: set -zone")
	}
	if len(zone) > 190 {
		return errors.New("zone is too long to contain a 63-character hostname")
	}
	for _, label := range strings.Split(strings.TrimSuffix(zone, "."), ".") {
		if !validLabel(label) {
			return fmt.Errorf("invalid zone %q", zone)
		}
	}
	if interval <= 0 {
		return errors.New("refresh interval must be positive")
	}
	if ttl < time.Second || ttl%time.Second != 0 || ttl/time.Second > math.MaxInt32 {
		return errors.New("DNS TTL must be a whole number of seconds between 1 and 2147483647")
	}
	return nil
}

// serve binds both transports before serving either, and does not return until
// both have stopped. The readiness handshake makes immediate shutdown safe.
func serve(ctx context.Context, address string, handler dns.Handler) error {
	var lc net.ListenConfig
	tcp, err := lc.Listen(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer tcp.Close()
	udp, err := lc.ListenPacket(ctx, "udp", tcp.Addr().String())
	if err != nil {
		return err
	}
	defer udp.Close()
	servers := []*dns.Server{{Listener: tcp, Handler: handler}, {PacketConn: udp, Handler: handler}}
	results := make(chan error, len(servers))
	for _, server := range servers {
		ready := make(chan struct{})
		markReady := sync.OnceFunc(func() { close(ready) })
		server.NotifyStartedFunc = markReady
		go func() {
			results <- server.ActivateAndServe()
			markReady() // Socket initialization can fail before the callback.
		}()
		<-ready
	}
	slog.Info("DNS listening", "address", tcp.Addr(), "transports", "udp,tcp")
	remaining := len(servers)
	select {
	case err = <-results:
		remaining--
	case <-ctx.Done():
	}
	slog.Info("DNS shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		err = errors.Join(err, server.ShutdownContext(shutdown))
	}
	for range remaining {
		err = errors.Join(err, <-results)
	}
	return err
}
