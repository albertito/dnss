//go:build ignore

// dnsprobe sends DNS queries to a server at a regular interval, and logs the
// result of each one. It's used by outage.sh to measure how dnss behaves
// during and after network outages.
//
// Each query is for a different name, so the answers can't come from a cache.
//
// It prints one line per query, as they complete:
//
//	<send time, in unix milliseconds> <latency in milliseconds> <result>
//
// The result is "ok" if we got a NOERROR or NXDOMAIN reply, or a short
// description of the failure otherwise.
package main

import (
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var (
	server   = flag.String("server", "127.0.0.1:1053", "DNS server to query")
	interval = flag.Duration("interval", 200*time.Millisecond,
		"time between queries")
	timeout = flag.Duration("timeout", 2*time.Second, "timeout for each query")
	domain  = flag.String("domain", "probe.test.", "domain to query under")
)

var outMu sync.Mutex

func main() {
	flag.Parse()

	c := &dns.Client{
		Net:     "udp",
		Timeout: *timeout,
	}

	// Use a random prefix, so names don't repeat across runs either.
	prefix := rand.Uint32()

	ticker := time.NewTicker(*interval)
	for i := 0; ; i++ {
		go probe(c, fmt.Sprintf("%x-%d.%s", prefix, i, *domain))
		<-ticker.C
	}
}

func probe(c *dns.Client, name string) {
	m := &dns.Msg{}
	m.SetQuestion(name, dns.TypeA)

	start := time.Now()
	r, _, err := c.Exchange(m, *server)
	latency := time.Since(start)

	result := "ok"
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		result = "timeout"
	case err != nil:
		result = "error: " + err.Error()
	case r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError:
		result = dns.RcodeToString[r.Rcode]
	}

	outMu.Lock()
	fmt.Fprintf(os.Stdout, "%d %d %s\n",
		start.UnixMilli(), latency.Milliseconds(), result)
	outMu.Unlock()
}
