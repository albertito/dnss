package dnstogrpc

import (
	"expvar"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"golang.org/x/net/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"bytes"

	pb "blitiri.com.ar/go/dnss/internal/proto"
)

// Interface for DNS resolvers that can answer queries.
type Resolver interface {
	// Initialize the resolver.
	Init() error

	// Maintain performs resolver maintenance. It's expected to run
	// indefinitely, but may return early if appropriate.
	Maintain()

	// Query responds to a DNS query.
	Query(r *dns.Msg, tr trace.Trace) (*dns.Msg, error)
}

// grpcResolver implements the Resolver interface by querying a server via
// GRPC.
type grpcResolver struct {
	Upstream string
	CAFile   string
	client   pb.DNSServiceClient
}

func NewGRPCResolver(upstream, caFile string) *grpcResolver {
	return &grpcResolver{
		Upstream: upstream,
		CAFile:   caFile,
	}
}

func (g *grpcResolver) Init() error {
	var err error
	var creds credentials.TransportAuthenticator
	if g.CAFile == "" {
		creds = credentials.NewClientTLSFromCert(nil, "")
	} else {
		creds, err = credentials.NewClientTLSFromFile(g.CAFile, "")
		if err != nil {
			return err
		}
	}

	conn, err := grpc.Dial(g.Upstream, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}

	g.client = pb.NewDNSServiceClient(conn)
	return nil
}

func (g *grpcResolver) Maintain() {
}

func (g *grpcResolver) Query(r *dns.Msg, tr trace.Trace) (*dns.Msg, error) {
	buf, err := r.Pack()
	if err != nil {
		return nil, err
	}

	// Give our RPCs 2 second timeouts: DNS usually doesn't wait that long
	// anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := g.client.Query(ctx, &pb.RawMsg{Data: buf})
	if err != nil {
		return nil, err
	}

	m := &dns.Msg{}
	err = m.Unpack(reply.Data)
	return m, err
}

// cachingResolver implements a caching Resolver.
// It is backed by another Resolver, but will cache results.
type cachingResolver struct {
	// Backing resolver.
	back Resolver

	// The cache where we keep the records.
	answer  map[dns.Question][]dns.RR
	expires map[dns.Question]time.Time

	// mu protects both answer and expires.
	mu *sync.RWMutex
}

func NewCachingResolver(back Resolver) *cachingResolver {
	return &cachingResolver{
		back:    back,
		answer:  map[dns.Question][]dns.RR{},
		expires: map[dns.Question]time.Time{},
		mu:      &sync.RWMutex{},
	}
}

const (
	// Maximum number of entries we keep in the cache.
	// 2k should be reasonable for a small network.
	// Keep in mind that increasing this too much will interact negatively
	// with Maintain().
	maxCacheSize = 2000

	// Minimum TTL for entries we consider for the cache.
	minTTL = 2 * time.Minute

	// Maximum TTL for our cache. We cap records that exceed this.
	maxTTL = 2 * time.Hour

	// How often to run GC on the cache.
	// Must be < minTTL if we don't want to have entries stale for too long.
	maintenancePeriod = 30 * time.Second
)

// Exported variables for statistics.
// These are global and not per caching resolver, so if we have more than once
// the results will be mixed.
var stats = struct {
	// Total number of queries handled by the cache resolver.
	cacheTotal *expvar.Int

	// Queries that we passed directly through our back resolver.
	cacheBypassed *expvar.Int

	// Cache misses.
	cacheMisses *expvar.Int

	// Cache hits.
	cacheHits *expvar.Int

	// Entries we decided to record in the cache.
	cacheRecorded *expvar.Int
}{}

func init() {
	stats.cacheTotal = expvar.NewInt("cache-total")
	stats.cacheBypassed = expvar.NewInt("cache-bypassed")
	stats.cacheHits = expvar.NewInt("cache-hits")
	stats.cacheMisses = expvar.NewInt("cache-misses")
	stats.cacheRecorded = expvar.NewInt("cache-recorded")
}

func (c *cachingResolver) Init() error {
	if err := c.back.Init(); err != nil {
		return err
	}

	// We register the debug handlers.
	// Note these are global by nature, if you create more than once resolver,
	// the last one will prevail.
	http.HandleFunc("/debug/dnstogrpc/cache/dump", c.DumpCache)
	http.HandleFunc("/debug/dnstogrpc/cache/flush", c.FlushCache)
	return nil
}

func (c *cachingResolver) DumpCache(w http.ResponseWriter, r *http.Request) {
	buf := bytes.NewBuffer(nil)
	now := time.Now().Truncate(time.Second)
	var expires time.Time

	c.mu.RLock()
	for q, ans := range c.answer {
		expires = c.expires[q].Truncate(time.Second)

		// Only include names and records if we are running verbosily.
		name := "<hidden>"
		if glog.V(3) {
			name = q.Name
		}

		fmt.Fprintf(buf, "Q: %s %s %s\n", name, dns.TypeToString[q.Qtype],
			dns.ClassToString[q.Qclass])

		fmt.Fprintf(buf, "   expires in %s (%s)\n", expires.Sub(now),
			expires)

		if glog.V(3) {
			for _, rr := range ans {
				fmt.Fprintf(buf, "   %s\n", rr.String())
			}
		} else {
			fmt.Fprintf(buf, "   %d RRs in answer\n", len(ans))
		}
		fmt.Fprintf(buf, "\n\n")
	}
	c.mu.RUnlock()

	buf.WriteTo(w)
}

func (c *cachingResolver) FlushCache(w http.ResponseWriter, r *http.Request) {

	c.mu.Lock()
	c.answer = map[dns.Question][]dns.RR{}
	c.expires = map[dns.Question]time.Time{}
	c.mu.Unlock()

	w.Write([]byte("cache flush complete"))
}

func (c *cachingResolver) Maintain() {
	go c.back.Maintain()

	for now := range time.Tick(maintenancePeriod) {
		tr := trace.New("dnstogrpc.Cache", "GC")
		var total, expired int

		c.mu.Lock()
		total = len(c.expires)
		for q, exp := range c.expires {
			if now.Before(exp) {
				continue
			}

			delete(c.answer, q)
			delete(c.expires, q)
			expired++
		}
		c.mu.Unlock()
		tr.LazyPrintf("total: %d   expired: %d", total, expired)
		tr.Finish()
	}
}

func wantToCache(question dns.Question, reply *dns.Msg) error {
	if reply.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("unsuccessful query")
	} else if !reply.Response {
		return fmt.Errorf("response = false")
	} else if reply.Opcode != dns.OpcodeQuery {
		return fmt.Errorf("opcode %d != query", reply.Opcode)
	} else if len(reply.Answer) == 0 {
		return fmt.Errorf("answer is empty")
	} else if len(reply.Question) != 1 {
		return fmt.Errorf("too many/few questions (%d)", len(reply.Question))
	} else if reply.Question[0] != question {
		return fmt.Errorf(
			"reply question does not match: asked %v, got %v",
			question, reply.Question[0])
	}

	return nil
}

func calculateTTL(answer []dns.RR) time.Duration {
	// This assumes all RRs have the same TTL.  That may not be the case in
	// theory, but we are ok not caring for this for now.
	ttl := time.Duration(answer[0].Header().Ttl) * time.Second

	// This helps prevent cache pollution due to unused but long entries, as
	// we don't do usage-based caching yet.
	if ttl > maxTTL {
		ttl = maxTTL
	}

	return ttl
}

func (c *cachingResolver) Query(r *dns.Msg, tr trace.Trace) (*dns.Msg, error) {
	stats.cacheTotal.Add(1)

	// To keep it simple we only cache single-question queries.
	if len(r.Question) != 1 {
		tr.LazyPrintf("cache bypass: multi-question query")
		stats.cacheBypassed.Add(1)
		return c.back.Query(r, tr)
	}

	question := r.Question[0]

	c.mu.RLock()
	answer, hit := c.answer[question]
	c.mu.RUnlock()

	if hit {
		tr.LazyPrintf("cache hit")
		stats.cacheHits.Add(1)

		reply := &dns.Msg{
			MsgHdr: dns.MsgHdr{
				Id:            r.Id,
				Response:      true,
				Authoritative: false,
				Rcode:         dns.RcodeSuccess,
			},
			Question: r.Question,
			Answer:   answer,
		}

		return reply, nil
	}

	tr.LazyPrintf("cache miss")
	stats.cacheMisses.Add(1)

	reply, err := c.back.Query(r, tr)
	if err != nil {
		return reply, err
	}

	if err = wantToCache(question, reply); err != nil {
		tr.LazyPrintf("cache not recording reply: %v", err)
		return reply, nil
	}

	answer = reply.Answer
	ttl := calculateTTL(answer)
	expires := time.Now().Add(ttl)

	// Only store answers if they're going to stay around for a bit,
	// there's not much point in caching things we have to expire quickly.
	if ttl > minTTL {
		// Override the answer TTL to our minimum.
		// Otherwise we'd be telling the clients high TTLs for as long as the
		// entry is in our cache.
		// This makes us very unsuitable as a proper DNS server, but it's
		// useful when we're the last ones and in a small network where
		// clients are unlikely to cache up to TTL anyway.
		for _, rr := range answer {
			rr.Header().Ttl = uint32(minTTL.Seconds())
		}

		// Store the answer in the cache, but don't exceed 2k entries.
		// TODO: Do usage based eviction when we're approaching ~1.5k.
		c.mu.Lock()
		if len(c.answer) < maxCacheSize {
			c.answer[question] = answer
			c.expires[question] = expires
			stats.cacheRecorded.Add(1)
		}
		c.mu.Unlock()
	}

	return reply, nil
}
