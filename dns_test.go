package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSAnswers(t *testing.T) {
	c := newCache(sourceFunc(func(context.Context) (*nodeMap, error) { return testNodes(), nil }))
	h := &zoneHandler{ctx: context.Background(), zone: "xn.io.", ttl: 300, cache: c}
	for _, tt := range []struct {
		name   string
		kind   uint16
		code   int
		answer string
		aa     bool
	}{
		{"freki.xn.io.", dns.TypeA, dns.RcodeSuccess, "freki.xn.io.\t300\tIN\tA\t100.64.0.2", true},
		{"freki.xn.io.", dns.TypeAAAA, dns.RcodeSuccess, "freki.xn.io.\t300\tIN\tAAAA\tfd7a:115c:a1e0::2", true},
		{"FrEkI.XN.IO.", dns.TypeA, dns.RcodeSuccess, "FrEkI.XN.IO.\t300\tIN\tA\t100.64.0.2", true},
		{"freki.xn.io", dns.TypeA, dns.RcodeSuccess, "freki.xn.io.\t300\tIN\tA\t100.64.0.2", true},
		{"v6.xn.io.", dns.TypeA, dns.RcodeSuccess, "", true},
		{"empty.xn.io.", dns.TypeAAAA, dns.RcodeSuccess, "", true},
		{"freki.xn.io.", dns.TypeTXT, dns.RcodeSuccess, "", true},
		{"unknown.xn.io.", dns.TypeA, dns.RcodeNameError, "", true},
		{"unknown.xn.io.", dns.TypeTXT, dns.RcodeNameError, "", true},
		{"nested.freki.xn.io.", dns.TypeA, dns.RcodeNameError, "", true},
		{"freki.notxn.io.", dns.TypeA, dns.RcodeRefused, "", false},
		{"freki.xn.io.evil.", dns.TypeA, dns.RcodeRefused, "", false},
		{"xn.io.", dns.TypeA, dns.RcodeSuccess, "", true},
		{"xn.io.", dns.TypeNS, dns.RcodeSuccess, "xn.io.\t300\tIN\tNS\tserver.xn.io.", true},
		{"xn.io.", dns.TypeSOA, dns.RcodeSuccess, "xn.io.\t30\tIN\tSOA\tserver.xn.io. hostmaster.xn.io. 1 300 30 86400 30", true},
		{"xn.io.", dns.TypeAXFR, dns.RcodeRefused, "", false},
		{"freki.xn.io.", dns.TypeANY, dns.RcodeRefused, "", false},
	} {
		t.Run(tt.name+"/"+dns.TypeToString[tt.kind], func(t *testing.T) {
			q := new(dns.Msg).SetQuestion(tt.name, tt.kind)
			r := h.answer(q)
			if r.Rcode != tt.code || r.Authoritative != tt.aa || r.RecursionAvailable || !r.Response || !r.RecursionDesired {
				t.Fatalf("incorrect reply flags: %+v", r.MsgHdr)
			}
			if tt.answer != "" {
				if len(r.Answer) != 1 || r.Answer[0].String() != tt.answer {
					t.Fatalf("answer %v, want %s", r.Answer, tt.answer)
				}
			} else if len(r.Answer) != 0 {
				t.Fatalf("unexpected answer: %v", r.Answer)
			}
			if tt.aa && tt.answer == "" {
				if len(r.Ns) != 1 {
					t.Fatal("negative response lacks SOA")
				}
				soa, ok := r.Ns[0].(*dns.SOA)
				if !ok || soa.Hdr.Ttl != 30 || soa.Minttl != 30 {
					t.Fatalf("incorrect negative TTL: %v", r.Ns)
				}
			}
		})
	}
}

func TestDNSFailures(t *testing.T) {
	c := newCache(sourceFunc(func(context.Context) (*nodeMap, error) { return nil, errors.New("offline") }))
	h := &zoneHandler{ctx: context.Background(), zone: "xn.io.", ttl: 300, cache: c}
	q := new(dns.Msg).SetQuestion("freki.xn.io.", dns.TypeA)
	if r := h.answer(q); r.Rcode != dns.RcodeServerFailure || r.Authoritative {
		t.Fatalf("initial source failure: %v", r)
	}
	c.nodes = testNodes()
	if r := h.answer(q); r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
		t.Fatalf("cached name not served during failure: %v", r)
	}
	q.SetQuestion("unknown.xn.io.", dns.TypeA)
	if r := h.answer(q); r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("unverified name should SERVFAIL: %v", r)
	}
	q.Question = nil
	if r := h.answer(q); r.Rcode != dns.RcodeFormatError {
		t.Fatalf("zero questions: %v", r)
	}
	q.SetQuestion("xn.io.", dns.TypeA)
	q.Question = append(q.Question, q.Question[0])
	if r := h.answer(q); r.Rcode != dns.RcodeFormatError {
		t.Fatalf("multiple questions: %v", r)
	}
	q.SetQuestion("xn.io.", dns.TypeA)
	q.Question[0].Qclass = dns.ClassCHAOS
	if r := h.answer(q); r.Rcode != dns.RcodeRefused {
		t.Fatalf("non-IN question: %v", r)
	}
	q.Opcode = dns.OpcodeUpdate
	if r := h.answer(q); r.Rcode != dns.RcodeNotImplemented {
		t.Fatalf("update request: %v", r)
	}
}

func TestConfig(t *testing.T) {
	for _, tt := range []struct {
		zone          string
		interval, ttl time.Duration
		valid         bool
	}{
		{"xn.io.", 5 * time.Minute, 5 * time.Minute, true},
		{"xn.io.", 0, 5 * time.Minute, false},
		{"xn.io.", time.Minute, time.Millisecond, false},
		{"", time.Minute, time.Minute, false},
		{".", time.Minute, time.Minute, false},
		{"a..io.", time.Minute, time.Minute, false},
		{"-a.io.", time.Minute, time.Minute, false},
	} {
		if err := validateConfig(tt.zone, tt.interval, tt.ttl); (err == nil) != tt.valid {
			t.Errorf("%+v: %v", tt, err)
		}
	}
}
