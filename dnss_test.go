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
	"blitiri.com.ar/go/dnss/internal/dnstohttps"
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

	// DNS to HTTPS server.
	HTTPSToDNSURL, err := url.Parse("http://" + HTTPSToDNSAddr + "/resolve")
	if err != nil {
		tb.Fatalf("invalid URL: %v", err)
	}

	var r dnsserver.Resolver
	if mode == "DoH" {
		r = dnstohttps.NewDoHResolver(HTTPSToDNSURL, "")
	} else {
		r = dnstohttps.NewJSONResolver(HTTPSToDNSURL, "")
	}
	dtoh := dnsserver.New(DNSToHTTPSAddr, r, "")
	go dtoh.ListenAndServe()

	// HTTPS to DNS server.
	htod := httpserver.Server{
		Addr:     HTTPSToDNSAddr,
		Upstream: DNSServerAddr,
	}
	httpserver.InsecureForTesting = true
	go htod.ListenAndServe()

	// Fake DNS server.
	go ServeFakeDNSServer(DNSServerAddr)

	// Wait for the servers to start up.
	err1 := testutil.WaitForDNSServer(DNSToHTTPSAddr)
	err2 := testutil.WaitForHTTPServer(HTTPSToDNSAddr)
	err3 := testutil.WaitForDNSServer(DNSServerAddr)
	if err1 != nil || err2 != nil || err3 != nil {
		tb.Logf("Error waiting for the test servers to start:\n")
		tb.Logf("  DNS to HTTPS: %v\n", err1)
		tb.Logf("  HTTPS to DNS: %v\n", err2)
		tb.Logf("  DNS server:   %v\n", err3)
		tb.Fatalf("Check the INFO logs for more details\n")
	}

	return DNSToHTTPSAddr
}

// Fake DNS server.
func ServeFakeDNSServer(addr string) {
	server := &dns.Server{
		Addr:    addr,
		Handler: dns.HandlerFunc(handleFakeDNS),
		Net:     "udp",
	}
	err := server.ListenAndServe()
	panic(err)
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
	for x := range dns.ParseZone(strings.NewReader(zone), "", "") {
		if x.Error != nil {
			tb.Fatalf("error parsing zone: %v\n", x.Error)
			return
		}

		hdr := x.RR.Header()
		key := fmt.Sprintf("%s %d", hdr.Name, hdr.Rrtype)
		answersMu.Lock()
		answers[key] = append(answers[key], x.RR)
		answersMu.Unlock()
	}
}

func handleFakeDNS(w dns.ResponseWriter, r *dns.Msg) {
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

// Test proxyServerDomain(). Unfortunately, this function can only be called
// once, as the results of http.ProxyFromEnvironment are cached, so we test it
// for a single case.
func TestProxyServerDomain(t *testing.T) {
	*httpsUpstream = "https://montoto/xyz"
	os.Setenv("HTTPS_PROXY", "http://proxy:1234/p")
	if got := proxyServerDomain(); got != "proxy" {
		t.Errorf("got %q, expected 'proxy'", got)
	}
}

func TestExtractHostname(t *testing.T) {
	cases := []struct{ host, expected string }{
		{"host", "host"},
		{"host:1234", "host"},
		{"[host]", "host"},
		{"[host]:1234", "host"},
		{"1.2.3.4", "1.2.3.4"},
		{"1.2.3.4:1234", "1.2.3.4"},
		{"[::192.9.5.5]", "::192.9.5.5"},
		{"[::192.9.5.5]:1234", "::192.9.5.5"},
		{"[3ffe:2a00:100:7031::1]", "3ffe:2a00:100:7031::1"},
		{"[3ffe:2a00:100:7031::1]:1234", "3ffe:2a00:100:7031::1"},
	}
	for _, c := range cases {
		if got := extractHostname(c.host); got != c.expected {
			t.Errorf("extractHostname(%q) = %q ; expected %q",
				c.host, got, c.expected)
		}
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
