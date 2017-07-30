// Package testutil implements common testing utilities.
package testutil

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

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

// Get a free (TCP) port. This is hacky and not race-free, but it works well
// enough for testing purposes.
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

// TestTrace implements the tracer.Trace interface, but prints using the test
// logging infrastructure.
type TestTrace struct {
	T *testing.T
}

func NewTestTrace(t *testing.T) *TestTrace {
	return &TestTrace{t}
}

func (t *TestTrace) LazyLog(x fmt.Stringer, sensitive bool) {
	t.T.Logf("trace %p (%b): %s", t, sensitive, x)
}

func (t *TestTrace) LazyPrintf(format string, a ...interface{}) {
	prefix := fmt.Sprintf("trace %p: ", t)
	t.T.Logf(prefix+format, a...)
}

func (t *TestTrace) SetError()                           {}
func (t *TestTrace) SetRecycler(f func(interface{}))     {}
func (t *TestTrace) SetTraceInfo(traceID, spanID uint64) {}
func (t *TestTrace) SetMaxEvents(m int)                  {}
func (t *TestTrace) Finish()                             {}

// NullTrace implements the tracer.Trace interface, but discards everything.
type NullTrace struct{}

func (t *NullTrace) LazyLog(x fmt.Stringer, sensitive bool)     {}
func (t *NullTrace) LazyPrintf(format string, a ...interface{}) {}
func (t *NullTrace) SetError()                                  {}
func (t *NullTrace) SetRecycler(f func(interface{}))            {}
func (t *NullTrace) SetTraceInfo(traceID, spanID uint64)        {}
func (t *NullTrace) SetMaxEvents(m int)                         {}
func (t *NullTrace) Finish()                                    {}
