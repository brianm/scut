# scut

A small authoritative DNS server that maps local Tailscale machine names into
one custom zone. `freki.xn.io` resolves to the Tailscale addresses of `freki`.

Scut uses the existing local `tailscaled` through Tailscale's official Go client.
It needs no API credentials, separate Tailscale node, database, or hostname list.
Its inventory contains this machine and the peers visible to its `tailscaled`,
not necessarily every device in the tailnet.

## Build and run

Requires Go 1.27.1 or newer (required by the pinned Tailscale dependency).

```sh
go build -o scut .
./scut -zone xn.io. -listen 127.0.0.1:1053
dig @127.0.0.1 -p 1053 freki.xn.io A
dig @127.0.0.1 -p 1053 freki.xn.io AAAA +tcp
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-zone` | required | The authoritative DNS zone; no built-in default |
| `-listen` | `:53` | Listen address for both UDP and TCP |
| `-tailscale-socket` | platform default | Override the local Tailscale socket |
| `-refresh-interval` | `5m` | Periodic inventory refresh interval |
| `-dns-ttl` | `5m` | Positive DNS answer TTL |
| `-debug` | `false` | Log refresh success and request errors |

Set the zone with `-zone`. Without it, scut exits with an error before contacting
Tailscale or opening listeners. Configuration uses command-line flags only.

The default socket on FreeBSD is `/var/run/tailscale/tailscaled.sock`.
On macOS the official client also supports the Tailscale app's local API.
An explicit socket override uses only that socket.

## Names and caching

- Load the full inventory at startup, then refresh every five minutes.
- Answer known names immediately from memory, including while a refresh runs.
- For an unknown short name, refresh and look again. Concurrent refreshes share
  one local API request. Confirmed misses are remembered for 30 seconds.
- Replace the entire inventory after a successful refresh. This removes names
  for deleted or renamed nodes. Offline nodes still have DNS records.
- On refresh failure, retain old addresses indefinitely. Queries for names with
  no cached answer or confirmed miss return `SERVFAIL` if refreshing fails.
  Startup remains available to recover after an initial local API failure.
- A local API request has a five-second timeout. Nothing is saved to disk.

Names come from the first label of Tailscale's assigned `DNSName`, including
the server's own name. Raw OS hostnames are not aliases. Matching ignores case
and trailing dots. Duplicate short labels make a refresh fail rather than
select an arbitrary machine. Only current network-map peers are included;
shared-to-user bookkeeping entries are excluded.

## DNS behavior

Scut answers A and AAAA records for single-label hosts in its zone. A known host
without the requested record type returns `NOERROR` with no answer (NODATA).
Unknown names and nested names return `NXDOMAIN`. Outside-zone queries and zone
transfers and ANY requests are refused. There is no recursion, forwarding, or
wildcard matching.

The zone apex has SOA and NS records naming the server's own mapped hostname;
it has no A or AAAA records. Negative answers include an SOA with a 30-second
negative TTL. Positive address records default to five minutes. Resolver caches
can therefore retain an old answer after scut has learned a change; negative
answers can also remain cached after scut's internal miss cache expires.

The listener is intended for the tailnet or another trusted network. `:53`
binds all interfaces; use `-listen` to bind a specific address. Configure
Tailscale split DNS for the zone to point at this server's Tailscale IP. Scut
does not change system DNS or Tailscale admin settings.

## FreeBSD

Build a binary on FreeBSD, or cross-compile:

```sh
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -trimpath -o scut .
```

Install the binary and service script:

```sh
install -m 755 scut /usr/local/sbin/scut
install -m 555 rc.d/scut /usr/local/etc/rc.d/scut
sysrc scut_enable=YES
sysrc scut_args='-zone xn.io. -listen :53'
service scut start
```

`scut_args` passes arguments to the binary. The equivalent `/etc/rc.conf` entries
are:

```sh
scut_enable="YES"
scut_args="-zone xn.io. -listen :53"
```

The service starts after networking and `tailscaled`, logs through syslog, and
runs as root to bind port 53 and access the local API. Both UDP and TCP listeners
shut down on SIGINT or SIGTERM. The rc script does not restart the daemon after
an exit.

## Tests

```sh
go test -race ./...
go vet ./...
```

Tests use an in-memory node source and a fake HTTP transport; they do not need a
running Tailscale daemon. Cache timing and concurrency tests use Go's virtual
clock rather than waiting minutes.

## License

Apache License 2.0. See [LICENSE](LICENSE).
