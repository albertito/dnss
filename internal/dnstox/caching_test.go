package dnstox

// Tests for the caching resolver.
// Note the other resolvers have more functional tests in the testing/
// directory.

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"blitiri.com.ar/go/dnss/testing/util"

	"github.com/miekg/dns"
	"golang.org/x/net/trace"
)

// A test resolver that we use as backing for the caching resolver under test.
type TestResolver struct {
	// Has this resolver been initialized?
	init bool

	// Maintain() sends a value over this channel.
	maintain chan bool

	// The last query we've seen.
	lastQuery *dns.Msg

	// What we will respond to queries.
	response  *dns.Msg
	respError error
}

func NewTestResolver() *TestResolver {
	return &TestResolver{
		maintain: make(chan bool, 1),
	}
}

func (r *TestResolver) Init() error {
	r.init = true
	return nil
}

func (r *TestResolver) Maintain() {
	r.maintain <- true
}

func (r *TestResolver) Query(req *dns.Msg, tr trace.Trace) (*dns.Msg, error) {
	r.lastQuery = req
	if r.response != nil {
		r.response.Question = req.Question
		r.response.Authoritative = true
	}
	return r.response, r.respError
}

//
// === Tests ===
//

// Test basic functionality.
func TestBasic(t *testing.T) {
	r := NewTestResolver()

	c := NewCachingResolver(r)

	c.Init()
	if !r.init {
		t.Errorf("caching resolver did not initialize backing")
	}

	resetStats()

	resp := queryA(t, c, "test. A 1.2.3.4", "test.", "1.2.3.4")
	if !statsEquals(1, 0, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}
	if !resp.Authoritative {
		t.Errorf("cache miss was not authoritative")
	}

	// Same query, should be cached.
	resp = queryA(t, c, "", "test.", "1.2.3.4")
	if !statsEquals(2, 1, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}
	if resp.Authoritative {
		t.Errorf("cache hit was authoritative")
	}
}

// Test TTL handling.
func TestTTL(t *testing.T) {
	r := NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	// Note we don't start c.Maintain() yet, as we don't want the background
	// TTL updater until later.

	// Test a record with a larger-than-max TTL (1 day).
	// The TTL of the response should be capped.
	resp := queryA(t, c, "test. 86400 A 1.2.3.4", "test.", "1.2.3.4")
	if !statsEquals(1, 0, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}
	if ttl := getTTL(resp.Answer); ttl != maxTTL {
		t.Errorf("expected max TTL (%v), got %v", maxTTL, ttl)
	}

	// Same query, should be cached, and TTL also capped.
	// As we've not enabled cache maintenance, we can be sure TTL == maxTTL.
	resp = queryA(t, c, "", "test.", "1.2.3.4")
	if !statsEquals(2, 1, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}
	if ttl := getTTL(resp.Answer); ttl != maxTTL {
		t.Errorf("expected max TTL (%v), got %v", maxTTL, ttl)
	}

	// To test that the TTL is reduced appropriately, set a small maintenance
	// period, and then repeatedly query the record. We should see its TTL
	// shrinking down within 1s.
	// Even though the TTL resolution in the protocol is in seconds, we don't
	// need to wait that much "thanks" to rounding artifacts.
	maintenancePeriod = 50 * time.Millisecond
	go c.Maintain()
	resetStats()

	// Check that the back resolver's Maintain() is called.
	select {
	case <-r.maintain:
		t.Log("Maintain() called")
	case <-time.After(1 * time.Second):
		t.Errorf("back resolver Maintain() was not called")
	}

	start := time.Now()
	for time.Since(start) < 1*time.Second {
		resp = queryA(t, c, "", "test.", "1.2.3.4")
		t.Logf("TTL %v", getTTL(resp.Answer))
		if ttl := getTTL(resp.Answer); ttl <= (maxTTL - 1*time.Second) {
			break
		}
		time.Sleep(maintenancePeriod)
	}
	if ttl := getTTL(resp.Answer); ttl > (maxTTL - 1*time.Second) {
		t.Errorf("expected maxTTL-1s, got %v", ttl)
	}
}

// Test that we don't cache failed queries.
func TestFailedQueries(t *testing.T) {
	r := NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	// Do two failed identical queries, check that both are cache misses.
	queryFail(t, c)
	if !statsEquals(1, 0, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}

	queryFail(t, c)
	if !statsEquals(2, 0, 2) {
		t.Errorf("bad stats: %v", dumpStats())
	}
}

// Test that we handle the cache filling up.
// Note this test is tied to the current behaviour of not doing any eviction
// when we're full, which is not ideal and will likely be changed in the
// future.
func TestCacheFull(t *testing.T) {
	r := NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	r.response = newReply(mustNewRR(t, "test. A 1.2.3.4"))

	// Do maxCacheSize+1 different requests.
	for i := 0; i < maxCacheSize+1; i++ {
		queryA(t, c, "", fmt.Sprintf("test%d.", i), "1.2.3.4")
		if !statsEquals(i+1, 0, i+1) {
			t.Errorf("bad stats: %v", dumpStats())
		}
	}

	// Query up to maxCacheSize, they should all be hits.
	resetStats()
	for i := 0; i < maxCacheSize; i++ {
		queryA(t, c, "", fmt.Sprintf("test%d.", i), "1.2.3.4")
		if !statsEquals(i+1, i+1, 0) {
			t.Errorf("bad stats: %v", dumpStats())
		}
	}

	// Querying maxCacheSize+1 should be a miss, because the cache was full.
	resetStats()
	queryA(t, c, "", fmt.Sprintf("test%d.", maxCacheSize), "1.2.3.4")
	if !statsEquals(1, 0, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}
}

// Test behaviour when the size of the cache is 0 (so users can disable it
// that way).
func TestZeroSize(t *testing.T) {
	r := NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	// Override the max cache size to 0.
	prevMaxCacheSize := maxCacheSize
	maxCacheSize = 0
	defer func() { maxCacheSize = prevMaxCacheSize }()

	r.response = newReply(mustNewRR(t, "test. A 1.2.3.4"))

	// Do 5 different requests.
	for i := 0; i < 5; i++ {
		queryA(t, c, "", fmt.Sprintf("test%d.", i), "1.2.3.4")
		if !statsEquals(i+1, 0, i+1) {
			t.Errorf("bad stats: %v", dumpStats())
		}
	}

	// Query them back, they should all be misses.
	resetStats()
	for i := 0; i < 5; i++ {
		queryA(t, c, "", fmt.Sprintf("test%d.", i), "1.2.3.4")
		if !statsEquals(i+1, 0, i+1) {
			t.Errorf("bad stats: %v", dumpStats())
		}
	}
}

//
// === Benchmarks ===
//

func BenchmarkCacheSimple(b *testing.B) {
	var err error

	r := NewTestResolver()
	r.response = newReply(mustNewRR(b, "test. A 1.2.3.4"))

	c := NewCachingResolver(r)
	c.Init()

	tr := &util.NullTrace{}
	req := newQuery("test.", dns.TypeA)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err = c.Query(req, tr)
		if err != nil {
			b.Errorf("query failed: %v", err)
		}
	}
}

//
// === Helpers ===
//

func resetStats() {
	stats.cacheTotal.Set(0)
	stats.cacheBypassed.Set(0)
	stats.cacheHits.Set(0)
	stats.cacheMisses.Set(0)
	stats.cacheRecorded.Set(0)
}

func statsEquals(total, hits, misses int) bool {
	return (stats.cacheTotal.String() == strconv.Itoa(total) &&
		stats.cacheHits.String() == strconv.Itoa(hits) &&
		stats.cacheMisses.String() == strconv.Itoa(misses))
}

func dumpStats() string {
	return fmt.Sprintf("(t:%v  h:%s  m:%v)",
		stats.cacheTotal, stats.cacheHits, stats.cacheMisses)
}

func queryA(t *testing.T, c *cachingResolver, rr, domain, expected string) *dns.Msg {
	// Set up the response from the given RR (if any).
	if rr != "" {
		back := c.back.(*TestResolver)
		back.response = newReply(mustNewRR(t, rr))
	}

	tr := util.NewTestTrace(t)
	defer tr.Finish()

	req := newQuery(domain, dns.TypeA)
	resp, err := c.Query(req, tr)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	a := resp.Answer[0].(*dns.A)
	if a.A.String() != expected {
		t.Errorf("expected %s, got %v", expected, a.A)
	}

	if !reflect.DeepEqual(req.Question, resp.Question) {
		t.Errorf("question mis-match: request %v, response %v",
			req.Question, resp.Question)
	}

	return resp
}

func queryFail(t *testing.T, c *cachingResolver) *dns.Msg {
	back := c.back.(*TestResolver)
	back.response = &dns.Msg{}
	back.response.Response = true
	back.response.Rcode = dns.RcodeNameError

	tr := util.NewTestTrace(t)
	defer tr.Finish()

	req := newQuery("doesnotexist.", dns.TypeA)
	resp, err := c.Query(req, tr)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	return resp
}

func mustNewRR(tb testing.TB, s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		tb.Fatalf("invalid RR %q: %v", s, err)
	}
	return rr
}

func newQuery(domain string, t uint16) *dns.Msg {
	m := &dns.Msg{}
	m.SetQuestion(domain, t)
	return m
}

func newReply(answer dns.RR) *dns.Msg {
	return &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Response:      true,
			Authoritative: false,
			Rcode:         dns.RcodeSuccess,
		},
		Answer: []dns.RR{answer},
	}
}
