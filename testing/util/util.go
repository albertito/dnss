// Package util implements common testing utilities.
package util

import (
	"fmt"
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

	after := time.After(5 * time.Second)
	tick := time.Tick(100 * time.Millisecond)
	select {
	case <-after:
		return fmt.Errorf("timed out")
	case <-tick:
		conn.SetDeadline(time.Now().Add(1 * time.Second))
		conn.WriteMsg(m)
		_, err := conn.ReadMsg()
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("not reachable")
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
