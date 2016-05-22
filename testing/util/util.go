// Package util implements common testing utilities.
package util

import (
	"fmt"
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
