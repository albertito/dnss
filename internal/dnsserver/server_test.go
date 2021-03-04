package dnsserver

import (
	"fmt"
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

	srv := New(testutil.GetFreePort(), res, unqUpstreamAddr)
	go srv.ListenAndServe()
	testutil.WaitForDNSServer(srv.Addr)

	query(t, srv.Addr, "response.test.", "1.1.1.1")
	query(t, srv.Addr, "unqualified.", "2.2.2.2")
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

func TestBadUpstreams(t *testing.T) {
	res := testutil.NewTestResolver()
	res.RespError = fmt.Errorf("response error for testing")

	// Get addresses but don't start the servers, so we get an error when
	// trying to reach them.
	unqUpstreamAddr := testutil.GetFreePort()

	srv := New(testutil.GetFreePort(), res, unqUpstreamAddr)
	go srv.ListenAndServe()
	testutil.WaitForDNSServer(srv.Addr)

	queryFailure(t, srv.Addr, "response.test.")
	queryFailure(t, srv.Addr, "unqualified.")
}

func queryFailure(t *testing.T, srv, domain string) {
	m, _, err := testutil.DNSQuery(srv, domain, dns.TypeA)
	if err != nil {
		t.Errorf("error querying %q: %v", domain, err)
	}

	if m.Rcode != dns.RcodeServerFailure {
		t.Errorf("query %q: expected SERVFAIL, got message: %v", domain, m)
	}
}
