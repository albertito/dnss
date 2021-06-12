// Package testutil implements common testing utilities.
package testutil

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"blitiri.com.ar/go/dnss/internal/trace"

	"github.com/miekg/dns"
)

// WaitForDNSServer waits 5 seconds for a DNS server to start, and returns an
// error if it fails to do so.
// It does this by repeatedly querying the DNS server until it either replies
// or times out. Note we do not do any validation of the reply.
func WaitForDNSServer(addr string) error {
	conn, err := dns.DialTimeout("udp", addr, 1*time.Second)
	if err != nil {
		return fmt.Errorf("dns.Dial error: %v", err)
	}
	defer conn.Close()

	m := &dns.Msg{}
	m.SetQuestion("unused.", dns.TypeA)

	deadline := time.Now().Add(5 * time.Second)
	tick := time.Tick(100 * time.Millisecond)

	for (<-tick).Before(deadline) {
		conn.SetDeadline(time.Now().Add(1 * time.Second))
		conn.WriteMsg(m)
		_, err := conn.ReadMsg()
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("timed out")
}

// WaitForHTTPServer waits 5 seconds for an HTTP server to start, and returns
// an error if it fails to do so.
// It does this by repeatedly querying the server until it either replies or
// times out.
func WaitForHTTPServer(addr string) error {
	c := http.Client{
		Timeout: 100 * time.Millisecond,
	}

	deadline := time.Now().Add(5 * time.Second)
	tick := time.Tick(100 * time.Millisecond)

	for (<-tick).Before(deadline) {
		_, err := c.Get("http://" + addr + "/testpoke")
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("timed out")
}

// GetFreePort returns a free TCP port. This is hacky and not race-free, but
// it works well enough for testing purposes.
func GetFreePort() string {
	l, _ := net.Listen("tcp", "localhost:0")
	defer l.Close()
	return l.Addr().String()
}

// DNSQuery is a convenient wrapper to issue simple DNS queries.
func DNSQuery(srv, addr string, qtype uint16) (*dns.Msg, dns.RR, error) {
	m := new(dns.Msg)
	m.SetQuestion(addr, qtype)
	in, err := dns.Exchange(m, srv)

	if err != nil {
		return nil, nil, err
	} else if len(in.Answer) > 0 {
		return in, in.Answer[0], nil
	} else {
		return in, nil, nil
	}
}

// TestResolver is a dnsserver.Resolver implementation for testing, so we can
// control its responses during tests.
type TestResolver struct {
	// Has this resolver been initialized?
	Initialized bool

	// Maintain() sends a value over this channel.
	MaintainC chan bool

	// The last query we've seen.
	LastQuery *dns.Msg

	// What we will respond to queries.
	Response  *dns.Msg
	RespError error
}

// NewTestResolver creates a new TestResolver with minimal initialization.
func NewTestResolver() *TestResolver {
	return &TestResolver{
		MaintainC: make(chan bool, 1),
	}
}

// Init the resolver.
func (r *TestResolver) Init() error {
	r.Initialized = true
	return nil
}

// Maintain the resolver.
func (r *TestResolver) Maintain() {
	r.MaintainC <- true
}

// Query handles the given query, returning the pre-recorded response.
func (r *TestResolver) Query(req *dns.Msg, tr *trace.Trace) (*dns.Msg, error) {
	r.LastQuery = req
	if r.Response != nil {
		r.Response.Question = req.Question
		r.Response.Authoritative = true
	}
	return r.Response, r.RespError
}

// ServeTestDNSServer starts the fake DNS server.
func ServeTestDNSServer(addr string, handler func(dns.ResponseWriter, *dns.Msg)) {
	server := &dns.Server{
		Addr:    addr,
		Handler: dns.HandlerFunc(handler),
		Net:     "udp",
	}
	err := server.ListenAndServe()
	panic(err)
}

// MakeStaticHandler for the DNS server. The given answer must be a valid
// zone.
func MakeStaticHandler(tb testing.TB, answer string) func(dns.ResponseWriter, *dns.Msg) {
	rr := NewRR(tb, answer)

	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := &dns.Msg{}
		m.SetReply(r)
		m.Answer = append(m.Answer, rr)
		w.WriteMsg(m)
	}
}

func NewRR(tb testing.TB, s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		tb.Fatalf("Error parsing RR for testing: %v", err)
	}
	return rr
}
