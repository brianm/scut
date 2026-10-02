package main

import (
	"context"
	"log/slog"
	"net"
	"strings"

	"github.com/miekg/dns"
)

// zoneHandler serves exactly one zone, with no recursion or forwarding.
type zoneHandler struct {
	ctx   context.Context
	zone  string // lowercase, fully qualified
	ttl   uint32
	cache *nodeCache
}

func (h *zoneHandler) ServeDNS(w dns.ResponseWriter, query *dns.Msg) {
	reply := h.answer(query)
	if _, tcp := w.RemoteAddr().(*net.TCPAddr); !tcp {
		size := uint16(dns.MinMsgSize)
		if opt := query.IsEdns0(); opt != nil {
			size = max(size, opt.UDPSize())
		}
		reply.Truncate(int(size))
	}
	if err := w.WriteMsg(reply); err != nil {
		slog.Debug("DNS response failed", "error", err)
	}
}

func (h *zoneHandler) answer(query *dns.Msg) *dns.Msg {
	r := new(dns.Msg)
	r.SetReply(query)
	if query.Opcode != dns.OpcodeQuery {
		r.Rcode = dns.RcodeNotImplemented
		return r
	}
	if len(query.Question) != 1 {
		slog.Debug("malformed DNS query", "questions", len(query.Question))
		r.Rcode = dns.RcodeFormatError
		return r
	}
	q := query.Question[0]
	name := strings.ToLower(dns.Fqdn(q.Name))
	if q.Qclass != dns.ClassINET || (name != h.zone && !strings.HasSuffix(name, "."+h.zone)) {
		r.Rcode = dns.RcodeRefused
		return r
	}
	if q.Qtype == dns.TypeAXFR || q.Qtype == dns.TypeIXFR || q.Qtype == dns.TypeANY {
		r.Rcode = dns.RcodeRefused
		return r
	}
	label := ""
	if name != h.zone {
		label = strings.TrimSuffix(name, "."+h.zone)
	}
	// Nested/invalid labels cannot exist in this zone. Fetch only authority
	// metadata for them, rather than refreshing Tailscale for impossible names.
	lookup := label
	if label != "" && !validLabel(label) {
		lookup = ""
	}
	nodes, err := h.cache.lookup(h.ctx, lookup)
	if err != nil {
		r.Rcode = dns.RcodeServerFailure
		return r
	}
	r.Authoritative = true
	ns := nodes.Self + "." + h.zone
	soa := &dns.SOA{
		Hdr: dns.RR_Header{Name: h.zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: uint32(negativeTTL.Seconds())},
		Ns:  ns, Mbox: "hostmaster." + h.zone,
		// No transfers or secondaries: the serial is fixed. SOA's negative
		// TTL stays short even though positive answers can live for minutes.
		Serial: 1, Refresh: 300, Retry: 30, Expire: 86400, Minttl: uint32(negativeTTL.Seconds()),
	}
	header := dns.RR_Header{Name: dns.Fqdn(q.Name), Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: h.ttl}
	if label == "" {
		switch q.Qtype {
		case dns.TypeSOA:
			r.Answer = append(r.Answer, soa)
		case dns.TypeNS:
			r.Answer = append(r.Answer, &dns.NS{Hdr: header, Ns: ns})
		}
	} else if ips, exists := nodes.Hosts[label]; exists {
		for _, ip := range ips {
			switch {
			case q.Qtype == dns.TypeA && ip.Is4():
				r.Answer = append(r.Answer, &dns.A{Hdr: header, A: net.IP(ip.AsSlice())})
			case q.Qtype == dns.TypeAAAA && ip.Is6():
				r.Answer = append(r.Answer, &dns.AAAA{Hdr: header, AAAA: net.IP(ip.AsSlice())})
			}
		}
	} else {
		r.Rcode = dns.RcodeNameError
	}
	if len(r.Answer) == 0 {
		r.Ns = append(r.Ns, soa)
	}
	return r
}
