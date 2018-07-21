// Tests for dnss (end to end tests, and main-specific helpers).
package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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
func Setup(tb testing.TB, mode string) string {
	DNSToHTTPSAddr := testutil.GetFreePort()
	HTTPSToDNSAddr := testutil.GetFreePort()
	DNSServerAddr := testutil.GetFreePort()

	// HTTPS to DNS server.
	htod := httpserver.Server{
		Addr:     HTTPSToDNSAddr,
		Upstream: DNSServerAddr,
	}
	httpserver.InsecureForTesting = true
	go htod.ListenAndServe()

	// Test DNS server.
	go testutil.ServeTestDNSServer(DNSServerAddr, handleTestDNS)

	// Wait for the above to start; the DNS to HTTPS server below needs them
	// up for protocol autodetection.
	if err := testutil.WaitForHTTPServer(HTTPSToDNSAddr); err != nil {
		tb.Fatalf("Error waiting for HTTPS to DNS server to start: %v", err)
	}
	if err := testutil.WaitForDNSServer(DNSServerAddr); err != nil {
		tb.Fatalf("Error waiting for testing DNS server to start: %v", err)
	}

	// DNS to HTTPS server.
	HTTPSToDNSURL, err := url.Parse("http://" + HTTPSToDNSAddr + "/resolve")
	if err != nil {
		tb.Fatalf("invalid URL: %v", err)
	}

	var r dnsserver.Resolver
	switch mode {
	case "DoH":
		r = httpresolver.NewDoH(HTTPSToDNSURL, "")
	case "JSON":
		r = httpresolver.NewJSON(HTTPSToDNSURL, "")
	case "autodetect":
		r = httpresolver.New(HTTPSToDNSURL, "")
	default:
		tb.Fatalf("%q is not a valid mode", mode)
	}

	dtoh := dnsserver.New(DNSToHTTPSAddr, r, "")
	go dtoh.ListenAndServe()

	if err := testutil.WaitForDNSServer(DNSToHTTPSAddr); err != nil {
		tb.Fatalf("Error waiting for DNS to HTTPS server to start: %v", err)
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
	t.Run("mode=JSON", func(t *testing.T) { testEndToEnd(t, "JSON") })
	t.Run("mode=DoH", func(t *testing.T) { testEndToEnd(t, "DoH") })
	t.Run("mode=autodetect", func(t *testing.T) { testEndToEnd(t, "autodetect") })
}

func testEndToEnd(t *testing.T, mode string) {
	ServerAddr := Setup(t, mode)
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
	ServerAddr := Setup(b, "DoH")
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

func TestProxyServerDomain(t *testing.T) {
	prevProxy, wasSet := os.LookupEnv("HTTPS_PROXY")

	// Valid case, proxy set.
	os.Setenv("HTTPS_PROXY", "http://proxy:1234/p")
	*httpsUpstream = "https://montoto/xyz"
	if got := proxyServerDomain(); got != "proxy" {
		t.Errorf("got %q, expected 'proxy'", got)
	}

	// Valid case, proxy not set.
	os.Unsetenv("HTTPS_PROXY")
	*httpsUpstream = "https://montoto/xyz"
	if got := proxyServerDomain(); got != "" {
		t.Errorf("got %q, expected ''", got)
	}

	// Invalid upstream URL.
	*httpsUpstream = "in%20valid:url"
	if got := proxyServerDomain(); got != "" {
		t.Errorf("got %q, expected ''", got)
	}

	// Invalid proxy.
	os.Setenv("HTTPS_PROXY", "invalid value")
	*httpsUpstream = "https://montoto/xyz"
	if got := proxyServerDomain(); got != "" {
		t.Errorf("got %q, expected ''", got)
	}

	if wasSet {
		os.Setenv("HTTPS_PROXY", prevProxy)
	}
}

func TestDumpFlags(t *testing.T) {
	flag.Parse()
	flag.Set("https_upstream", "https://montoto/xyz")

	f := dumpFlags()
	if !strings.Contains(f, "-https_upstream=https://montoto/xyz\n") {
		t.Errorf("Flags string missing canary value: %v", f)
	}
}

func TestMonitoringServer(t *testing.T) {
	addr := testutil.GetFreePort()
	launchMonitoringServer(addr)
	testutil.WaitForHTTPServer(addr)

	checkGet(t, "http://"+addr+"/")
	checkGet(t, "http://"+addr+"/debug/requests")
	checkGet(t, "http://"+addr+"/debug/pprof/goroutine")
	checkGet(t, "http://"+addr+"/debug/flags")
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
