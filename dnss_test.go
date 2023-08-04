// Tests for dnss (end to end tests, and main-specific helpers).
package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"

	"blitiri.com.ar/go/dnss/internal/dnsserver"
	"blitiri.com.ar/go/dnss/internal/httpresolver"
	"blitiri.com.ar/go/dnss/internal/httpserver"
	"blitiri.com.ar/go/dnss/internal/testutil"
	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"
)

// Custom test main, so we reduce the default logging to avoid overly verbose
// tests.
func TestMain(m *testing.M) {
	flag.Parse()
	log.Init()
	log.Default.Level = log.Error

	os.Exit(m.Run())
}

/////////////////////////////////////////////////////////////////////
// End to end tests

// Setup the following environment:
// DNS client -> DNS-to-HTTPS -> HTTPS-to-DNS -> DNS server
//
// The DNS client will be created on each test.
// The DNS server will be created below, and the tests can adjust its
// responses as needed.
//
// Returns the address of the DNS-to-HTTPS server, for the tests to use.
func Setup(tb testing.TB) string {
	DNSToHTTPSAddr := testutil.GetFreePort()
	HTTPSToDNSAddr := testutil.GetFreePort()
	DNSServerAddr := testutil.GetFreePort()

	// HTTPS to DNS server.
	htod := httpserver.Server{
		Addr:     HTTPSToDNSAddr,
		Upstream: DNSServerAddr,
		Insecure: true,
	}
	go htod.ListenAndServe()

	// Test DNS server.
	go testutil.ServeTestDNSServer(DNSServerAddr, handleTestDNS)

	// DNS to HTTPS server.
	HTTPSToDNSURL, err := url.Parse("http://" + HTTPSToDNSAddr + "/resolve")
	if err != nil {
		tb.Fatalf("invalid URL: %v", err)
	}

	// Create the DoH resolver and DNS server backed by it.
	// Note that we use an invalid address as fallback resolver - since we use
	// IP addresses directly in the http requests, the fallback resolver
	// should not be needed.
	r := httpresolver.NewDoH(HTTPSToDNSURL, "", "0.0.0.0:0")
	dtoh := dnsserver.New(DNSToHTTPSAddr, r, "", nil)
	go dtoh.ListenAndServe()

	if err := testutil.WaitForDNSServer(DNSToHTTPSAddr); err != nil {
		tb.Fatalf("Error waiting for DNS to HTTPS server to start: %v", err)
	}
	if err := testutil.WaitForHTTPServer(HTTPSToDNSAddr); err != nil {
		tb.Fatalf("Error waiting for HTTPS to DNS server to start: %v", err)
	}
	if err := testutil.WaitForDNSServer(DNSServerAddr); err != nil {
		tb.Fatalf("Error waiting for testing DNS server to start: %v", err)
	}

	return DNSToHTTPSAddr
}

// DNS answers to give, as a map of "name type" -> []RR.
// Tests will modify this according to their needs.
var answers map[string][]dns.RR
var answersMu sync.Mutex

func resetAnswers() {
	answersMu.Lock()
	answers = map[string][]dns.RR{}
	answersMu.Unlock()
}

func addAnswers(tb testing.TB, zone string) {
	rr := testutil.NewRR(tb, zone)
	hdr := rr.Header()
	key := fmt.Sprintf("%s %d", hdr.Name, hdr.Rrtype)

	answersMu.Lock()
	answers[key] = append(answers[key], rr)
	answersMu.Unlock()
}

func handleTestDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := &dns.Msg{}
	m.SetReply(r)

	if len(r.Question) != 1 {
		w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	if testing.Verbose() {
		fmt.Printf("fake dns <- %v\n", q)
	}

	key := fmt.Sprintf("%s %d", q.Name, q.Qtype)
	answersMu.Lock()
	if rrs, ok := answers[key]; ok {
		m.Answer = rrs
	} else {
		m.Rcode = dns.RcodeNameError
	}
	answersMu.Unlock()

	if testing.Verbose() {
		fmt.Printf("fake dns -> %v | %v\n",
			dns.RcodeToString[m.Rcode], m.Answer)
	}
	w.WriteMsg(m)
}

//
// Tests
//

func TestEndToEnd(t *testing.T) {
	ServerAddr := Setup(t)
	resetAnswers()
	addAnswers(t, "test.blah. 3600 A 1.2.3.4")
	_, ans, err := testutil.DNSQuery(ServerAddr, "test.blah.", dns.TypeA)
	if err != nil {
		t.Errorf("dns query returned error: %v", err)
	}
	if ans.(*dns.A).A.String() != "1.2.3.4" {
		t.Errorf("unexpected result: %q", ans)
	}

	addAnswers(t, "test.blah. 3600 MX 10 mail.test.blah.")
	_, ans, err = testutil.DNSQuery(ServerAddr, "test.blah.", dns.TypeMX)
	if err != nil {
		t.Errorf("dns query returned error: %v", err)
	}
	if ans.(*dns.MX).Mx != "mail.test.blah." {
		t.Errorf("unexpected result: %q", ans.(*dns.MX).Mx)
	}

	in, _, err := testutil.DNSQuery(ServerAddr, "unknown.", dns.TypeA)
	if err != nil {
		t.Errorf("dns query returned error: %v", err)
	}
	if in.Rcode != dns.RcodeNameError {
		t.Errorf("unexpected result: %q", in)
	}
}

//
// Benchmarks
//

func BenchmarkSimple(b *testing.B) {
	ServerAddr := Setup(b)
	resetAnswers()
	addAnswers(b, "test.blah. 3600 A 1.2.3.4")
	b.ResetTimer()

	var err error
	for i := 0; i < b.N; i++ {
		_, _, err = testutil.DNSQuery(ServerAddr, "test.blah.", dns.TypeA)
		if err != nil {
			b.Errorf("dns query returned error: %v", err)
		}
	}
}

/////////////////////////////////////////////////////////////////////
// Tests for main-specific helpers

func TestMonitoringServer(t *testing.T) {
	addr := testutil.GetFreePort()
	go monitoringServer(addr)
	testutil.WaitForHTTPServer(addr)

	checkGet(t, "http://"+addr+"/")
	checkGet(t, "http://"+addr+"/debug/traces")
	checkGet(t, "http://"+addr+"/debug/pprof/goroutine")
	checkGet(t, "http://"+addr+"/debug/vars")

	// Check that we emit 404 for non-existing paths.
	r, _ := http.Get("http://" + addr + "/doesnotexist")
	if r.StatusCode != 404 {
		t.Errorf("expected 404, got %s", r.Status)
	}
}

func checkGet(t *testing.T, url string) {
	r, err := http.Get(url)
	if err != nil {
		t.Error(err)
		return
	}

	if r.StatusCode != 200 {
		t.Errorf("%q - invalid status: %s", url, r.Status)
	}

}
