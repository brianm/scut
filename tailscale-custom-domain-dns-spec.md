# Tailscale Custom Domain DNS Daemon

## Agreed v1 refinements

The discussion after this initial draft established the following changes.
These supersede conflicting suggestions below; `README.md` describes the
implemented behavior.

- Implement in Go; name the binary `scut`.
- Require a zone through `-zone`. Configuration is flag-based, with `scut_args`
  in `/etc/rc.conf` for the FreeBSD service. There is no built-in default domain;
  `xn.io` below is only an example.
- Use the short label from Tailscale's assigned `DNSName`, not raw OS hostnames.
- Load the complete inventory at startup and refresh every five minutes.
- Known names are answered immediately from memory. Unknown short names cause
  a refresh and a second lookup; concurrent refreshes share one operation.
- Remember confirmed missing names for 30 seconds. Local API errors do not
  count as confirmation that a name is absent.
- Successful refreshes replace the complete map. Failed refreshes retain the
  previous map indefinitely; there is no maximum stale-data age.
- When neither a cached answer nor a confirmed miss exists, a failed refresh
  produces `SERVFAIL`.
- Default positive DNS TTL: five minutes, configurable with `-dns-ttl`.
- Periodic refresh is configurable with `-refresh-interval` (replacing the
  draft's suggested lazy `-cache-ttl`).
- Include minimal apex SOA/NS records and an SOA on negative answers. Negative
  DNS TTL is 30 seconds, separate from the positive TTL.

## Goal

Build a very small standalone DNS server for FreeBSD that maps Tailscale machine names into a custom DNS suffix.

Primary behavior:

```text
{name}.xn.io -> current Tailscale IP(s) for machine {name}
```

Example:

```text
freki.xn.io   -> 100.x.y.z
pancake.xn.io -> 100.a.b.c
```

The daemon must discover machines dynamically from the local `tailscaled` instance. It must not require preconfigured hostnames or IP addresses.

## Design constraints

Keep this deliberately narrow.

Do not build a general-purpose DNS server.

Do not use:

- Docker
- CoreDNS
- the public Tailscale API
- Tailscale OAuth credentials
- Tailscale auth keys
- a second `tsnet` node
- periodic polling of the Tailscale control-plane API

Use the local Tailscale API through the existing `tailscaled` Unix socket.

Deployment target is primarily FreeBSD.

Preferred implementation language: Go.

The result should ideally be:

- one small static-ish binary
- one minimal config file or a handful of command-line flags
- one FreeBSD `rc.d` script

## DNS behavior

The daemon is authoritative for one configured DNS suffix, initially:

```text
xn.io.
```

Given a query for:

```text
freki.xn.io.
```

extract:

```text
freki
```

and resolve it against the current local Tailscale status.

Supported record types:

```text
A
AAAA
```

For known Tailscale nodes:

- return the current Tailscale IPv4 address for `A`
- return the current Tailscale IPv6 address for `AAAA`, if available

For a known node that does not have the requested address family:

```text
NOERROR / NODATA
```

For an unknown hostname beneath the managed zone:

```text
NXDOMAIN
```

For queries outside the configured zone, prefer:

```text
REFUSED
```

since the daemon is not intended to resolve arbitrary DNS names.

No recursive resolution or forwarding is required.

## Name matching

Use Tailscale's machine/host name as the short DNS label.

For example, if Tailscale status reports a machine as:

```text
freki
```

or a DNS name equivalent to:

```text
freki.<tailnet-name>.ts.net
```

the local DNS name should be:

```text
freki.xn.io.
```

Matching should be case-insensitive.

Normalize trailing dots.

Do not require users to manually define aliases.

For v1, only support a single DNS label before the configured zone:

```text
freki.xn.io
```

Do not support arbitrary nested names such as:

```text
foo.bar.xn.io
```

unless doing so falls out naturally and remains trivial.

## Tailscale integration

Use the local Tailscale API exposed by the existing `tailscaled` process.

Prefer Tailscale's official Go client/package if there is a stable public API for accessing local status.

If not, communicate with the local API directly over the Unix socket.

Expected source of truth is equivalent to:

```text
/localapi/v0/status
```

Do not shell out to:

```text
tailscale status
```

unless there is no practical library/local-API alternative.

### Socket path

Do not hardcode Linux-only paths.

Support a configurable socket path.

Determine a sensible FreeBSD default from the Tailscale package/runtime environment if practical.

Example flag/config:

```text
-tailscale-socket /var/run/tailscale/tailscaled.sock
```

If the official Tailscale Go local-client package already handles platform-specific socket paths, prefer that.

## Caching

Do not query `tailscaled` independently for every DNS packet.

Maintain an in-memory map:

```text
short-hostname -> []netip.Addr
```

Refresh it lazily or periodically from the local API.

Default cache lifetime:

```text
10 seconds
```

Anything in roughly the 5–30 second range is acceptable.

The important properties are:

- changes propagate quickly
- no external polling
- DNS queries are cheap
- stale data lifetime is bounded

Suggested approach:

1. DNS query arrives.
2. Check cache timestamp.
3. If cache is fresh, answer immediately.
4. If stale, refresh once from `tailscaled`.
5. Replace the complete in-memory map atomically.
6. Answer query.

Avoid a thundering herd when multiple requests arrive after expiration. A mutex or `singleflight` is sufficient.

If cache refresh fails:

- if a previous valid cache exists, continue serving it temporarily
- log the refresh error
- do not destroy the existing cache

If there has never been a successful refresh:

- return `SERVFAIL`

## DNS TTL

Default response TTL:

```text
30 seconds
```

Make it configurable if trivial.

The TTL should remain relatively short because Tailscale addresses or membership may change.

## DNS library

Using:

```text
github.com/miekg/dns
```

is fine.

Listen on both UDP and TCP.

Default:

```text
:53
```

Make the listen address configurable.

Example:

```text
-listen :53
```

## Configuration

Keep configuration minimal.

Command-line flags are sufficient for v1.

Suggested flags:

```text
-zone xn.io.
-listen :53
-tailscale-socket <path>
-cache-ttl 10s
-dns-ttl 30s
```

Possibly:

```text
-log-level
```

but only if logging infrastructure remains lightweight.

No database.

No YAML framework.

No large configuration dependency.

A TOML config file is acceptable only if it genuinely simplifies FreeBSD service deployment, but flags alone are preferred initially.

## Logging

Use Go's standard `log/slog`.

Log:

- startup configuration
- successful Tailscale cache refresh at debug level
- local API failures
- DNS server startup/shutdown
- malformed/unexpected queries at debug level

Do not log every normal DNS request by default.

## Shutdown

Handle:

```text
SIGINT
SIGTERM
```

Gracefully shut down both UDP and TCP DNS listeners.

## Security assumptions

The DNS server is intended to be reachable only over the tailnet or another trusted network.

It should not provide recursion.

Only answer for the configured zone.

Treat data returned by local `tailscaled` as authoritative.

No authentication layer is required in this daemon.

## FreeBSD deployment

Produce an example FreeBSD `rc.d` script.

Expected style:

```text
/usr/local/sbin/tailscale-dns
```

Configuration could come from `/etc/rc.conf`, e.g.:

```text
tailscale_dns_enable="YES"
tailscale_dns_zone="xn.io."
tailscale_dns_listen=":53"
```

The service should depend on networking and ideally start after `tailscaled`.

Do not overengineer the rc script.

## Tailscale configuration outside the daemon

This project does not need to modify Tailscale admin configuration itself.

Expected deployment procedure:

1. Run this daemon on one tailnet host.
2. Give that host a stable Tailscale identity.
3. In Tailscale DNS settings, configure split DNS for:

```text
xn.io
```

4. Point that split DNS zone at the Tailscale IP of the host running this daemon.

Then:

```text
freki.xn.io
```

should resolve to the current Tailscale address of `freki`.

## Testing

Unit-test at least:

- zone matching
- hostname extraction
- A lookup
- AAAA lookup
- unknown host -> NXDOMAIN
- known host with wrong address family -> NODATA
- outside zone -> REFUSED
- case-insensitive matching
- trailing-dot normalization
- stale cache refresh behavior
- failed refresh preserves previous cache
- first refresh failure -> SERVFAIL

Abstract the Tailscale-status provider behind a small interface so tests do not require a running `tailscaled`.

For example:

```go
type NodeSource interface {
    Nodes(ctx context.Context) (map[string][]netip.Addr, error)
}
```

Then implement:

```text
TailscaleLocalSource
```

for production and a fake source for tests.

## Non-goals

Do not implement in v1:

- wildcard DNS
- DNS-over-HTTPS
- DNS-over-TLS
- recursive DNS
- forwarding
- public Tailscale API support
- OAuth
- auth keys
- dynamic DNS updates
- web UI
- REST API
- persistent database
- per-host configuration
- service aliases
- public ingress/gateway functionality
- synchronization to external DNS providers

A future project may expose the same `*.xn.io` names publicly through a fixed ingress/gateway IP. That is explicitly separate from this daemon.

## Desired result

The final codebase should feel like a Unix utility, not an infrastructure platform.

Ideal usage:

```sh
tailscale-dns \
    -zone xn.io. \
    -listen :53
```

with the daemon discovering everything else automatically from local `tailscaled`.

The implementation should remain small enough that the whole design can be understood quickly by reading the source.

## First thing to verify

Before implementation, verify the current FreeBSD socket/local-client behavior in Tailscale's Go packages. That is the only part likely to need platform-specific care.
