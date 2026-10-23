package dnsserver

import (
	"bytes"
	"expvar"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"blitiri.com.ar/go/dnss/internal/trace"

	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"
)

// Resolver is the interface for DNS resolvers that can answer queries.
type Resolver interface {
	// Initialize the resolver.
	Init() error

	// Maintain performs resolver maintenance. It's expected to run
	// indefinitely, but may return early if appropriate.
	Maintain()

	// Query responds to a DNS query.
	Query(r *dns.Msg, tr *trace.Trace) (*dns.Msg, error)
}

///////////////////////////////////////////////////////////////////////////
// Caching resolver.

// cachingResolver implements a caching Resolver.
// It is backed by another Resolver, but will cache results.
type cachingResolver struct {
	// Backing resolver.
	back Resolver

	// The cache where we keep the records.
	answer map[cacheKey][]dns.RR

	// mu protects the answer map.
	mu *sync.RWMutex
}

// cacheKey is the key for the cache entries.
// Besides the question, it includes the flags that change the upstream's
// answer, so we don't serve an answer to clients that asked differently.
type cacheKey struct {
	q dns.Question

	// DNSSEC OK: the answer includes DNSSEC records.
	do bool

	// Checking Disabled: the answer was not validated by the upstream.
	cd bool
}

func newCacheKey(r *dns.Msg) cacheKey {
	opt := r.IsEdns0()
	return cacheKey{
		q:  r.Question[0],
		do: opt != nil && opt.Do(),
		cd: r.CheckingDisabled,
	}
}

func (k cacheKey) String() string {
	s := fmt.Sprintf("%s %s %s", k.q.Name,
		dns.TypeToString[k.q.Qtype], dns.ClassToString[k.q.Qclass])
	if k.do {
		s += " do"
	}
	if k.cd {
		s += " cd"
	}
	return s
}

// NewCachingResolver returns a new resolver which implements a cache on top
// of the given one.
func NewCachingResolver(back Resolver) *cachingResolver {
	return &cachingResolver{
		back:   back,
		answer: map[cacheKey][]dns.RR{},
		mu:     &sync.RWMutex{},
	}
}

// Constants that tune the cache.
// They are declared as variables so we can tweak them for testing.
var (
	// Maximum number of entries we keep in the cache.
	// 2k should be reasonable for a small network.
	// Keep in mind that increasing this too much will interact negatively
	// with Maintain().
	maxCacheSize = 2000

	// Maximum size of an answer we will cache, in bytes (uncompressed).
	// Larger answers are rare, and in memory they can be much bigger than
	// on the wire (each RR gets its own copy of the name), so caching them
	// could use a lot of memory.
	maxAnswerSize = 4 * 1024

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
	return c.back.Init()
}

// RegisterDebugHandlers registers http debug handlers, which can be accessed
// from the monitoring server.
// Note these are global by nature, if you try to register them multiple
// times, you will get a panic.
func (c *cachingResolver) RegisterDebugHandlers() {
	http.HandleFunc("/debug/dnsserver/cache/dump", c.DumpCache)
	http.HandleFunc("/debug/dnsserver/cache/flush", c.FlushCache)
}

func (c *cachingResolver) DumpCache(w http.ResponseWriter, r *http.Request) {
	buf := bytes.NewBuffer(nil)

	c.mu.RLock()

	// Sort output by expiration, so it is somewhat consistent and practical
	// to read.
	keys := []cacheKey{}
	for k := range c.answer {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return getTTL(c.answer[keys[i]]) < getTTL(c.answer[keys[j]])
	})

	// Go through the sorted list and dump the entries.
	for _, k := range keys {
		ans := c.answer[k]

		// Only include names and records if we are running verbosily.
		if !log.V(1) {
			k.q.Name = "<hidden>"
		}

		fmt.Fprintf(buf, "Q: %s\n", k)

		ttl := getTTL(ans)
		fmt.Fprintf(buf, "   expires in %s (%s)\n", ttl, time.Now().Add(ttl))

		if log.V(1) {
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
	c.answer = map[cacheKey][]dns.RR{}
	c.mu.Unlock()

	w.Write([]byte("cache flush complete"))
}

func (c *cachingResolver) Maintain() {
	go c.back.Maintain()

	for range time.Tick(maintenancePeriod) {
		tr := trace.New("dnsserver.Cache", "GC")
		var total, expired int

		c.mu.Lock()
		total = len(c.answer)
		for k, ans := range c.answer {
			newTTL := getTTL(ans) - maintenancePeriod
			if newTTL > 0 {
				// Don't modify in place, create a copy and override.
				// That way, we avoid races with users that have gotten a
				// cached answer and are returning it.
				newans := copyRRSlice(ans)
				setTTL(newans, newTTL)
				c.answer[k] = newans
				continue
			}

			delete(c.answer, k)
			expired++
		}
		c.mu.Unlock()
		tr.Printf("total: %d   expired: %d", total, expired)
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
	} else if reply.Truncated {
		return fmt.Errorf("truncated reply")
	} else if reply.Question[0] != question {
		return fmt.Errorf(
			"reply question does not match: asked %v, got %v",
			question, reply.Question[0])
	} else if size := answerSize(reply.Answer); size > maxAnswerSize {
		return fmt.Errorf("answer too large (%d bytes)", size)
	}

	return nil
}

// answerSize returns the uncompressed size of the answer, which is a
// reasonable approximation of how much memory it takes.
func answerSize(answer []dns.RR) int {
	size := 0
	for _, rr := range answer {
		size += dns.Len(rr)
	}
	return size
}

func limitTTL(answer []dns.RR) time.Duration {
	// Use the minimum TTL of all the RRs, as the answer is only valid as
	// long as all of them are. For example, a long-lived CNAME pointing to a
	// short-lived A record.
	//
	// Starting from maxTTL caps long TTLs. This helps prevent cache
	// pollution due to unused but long entries, as we don't do usage-based
	// caching yet.
	ttl := maxTTL
	for _, rr := range answer {
		rrTTL := rr.Header().Ttl

		// RFC 2181 section 8: TTLs with the most significant bit set
		// should be treated as 0.
		if rrTTL > math.MaxInt32 {
			rrTTL = 0
		}

		if t := time.Duration(rrTTL) * time.Second; t < ttl {
			ttl = t
		}
	}

	return ttl
}

func getTTL(answer []dns.RR) time.Duration {
	// This assumes all RRs have the same TTL, which is true for the cached
	// answers because we set it with setTTL.
	return time.Duration(answer[0].Header().Ttl) * time.Second
}

func setTTL(answer []dns.RR, newTTL time.Duration) {
	for _, rr := range answer {
		rr.Header().Ttl = uint32(newTTL.Seconds())
	}
}

func copyRRSlice(a []dns.RR) []dns.RR {
	b := make([]dns.RR, 0, len(a))
	for _, rr := range a {
		b = append(b, dns.Copy(rr))
	}
	return b
}

func (c *cachingResolver) Query(r *dns.Msg, tr *trace.Trace) (*dns.Msg, error) {
	stats.cacheTotal.Add(1)

	// To keep it simple we only cache single-question queries.
	if len(r.Question) != 1 {
		tr.Printf("cache bypass: multi-question query")
		stats.cacheBypassed.Add(1)
		return c.back.Query(r, tr)
	}

	question := r.Question[0]
	key := newCacheKey(r)

	c.mu.RLock()
	answer, hit := c.answer[key]
	c.mu.RUnlock()

	if hit {
		tr.Printf("cache hit")
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

	tr.Printf("cache miss")
	stats.cacheMisses.Add(1)

	reply, err := c.back.Query(r, tr)
	if err != nil {
		return reply, err
	}

	if err = wantToCache(question, reply); err != nil {
		tr.Printf("cache not recording reply: %v", err)
		return reply, nil
	}

	answer = reply.Answer
	ttl := limitTTL(answer)

	// Only store answers if they're going to stay around for a bit,
	// there's not much point in caching things we have to expire quickly.
	if ttl < minTTL {
		return reply, nil
	}

	// Store the answer in the cache, but don't exceed 2k entries.
	// TODO: Do usage based eviction when we're approaching ~1.5k.
	c.mu.Lock()
	if len(c.answer) < maxCacheSize {
		setTTL(answer, ttl)
		c.answer[key] = answer
		stats.cacheRecorded.Add(1)
	}
	c.mu.Unlock()

	return reply, nil
}

// Compile-time check that the implementation matches the interface.
var _ Resolver = &cachingResolver{}
