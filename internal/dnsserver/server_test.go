package dnsserver

import (
	"testing"

	"github.com/miekg/dns"

	"blitiri.com.ar/go/dnss/internal/testutil"
)

// Tests for the DNS server.

func TestServe(t *testing.T) {
	res := testutil.NewTestResolver()
	res.Response = &dns.Msg{
		Answer: []dns.RR{testutil.NewRR(t, "response.test. A 1.1.1.1")},
	}

	unqUpstreamAddr := testutil.GetFreePort()
	go testutil.ServeTestDNSServer(unqUpstreamAddr,
		testutil.MakeStaticHandler(t, "unq. A 2.2.2.2"))

	fallbackAddr := testutil.GetFreePort()
	go testutil.ServeTestDNSServer(fallbackAddr,
		testutil.MakeStaticHandler(t, "fallback. A 3.3.3.3"))

	srv := New(testutil.GetFreePort(), res, unqUpstreamAddr)
	srv.SetFallback(fallbackAddr, []string{"one.fallback.", "two.fallback."})
	go srv.ListenAndServe()
	testutil.WaitForDNSServer(srv.Addr)

	query(t, srv.Addr, "response.test.", "1.1.1.1")
	query(t, srv.Addr, "unqualified.", "2.2.2.2")
	query(t, srv.Addr, "one.fallback.", "3.3.3.3")
	query(t, srv.Addr, "two.fallback.", "3.3.3.3")
}

func query(t *testing.T, srv, domain, expected string) {
	_, rr, err := testutil.DNSQuery(srv, domain, dns.TypeA)
	if err != nil {
		t.Errorf("error querying %q: %v", domain, err)
		return
	}

	result := rr.(*dns.A).A.String()
	if result != expected {
		t.Errorf("query %q: expected %q but got %q", domain, expected, result)
	}
}
