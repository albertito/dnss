package dnsserver

// Tests for the caching resolver.

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"blitiri.com.ar/go/dnss/internal/testutil"
	"blitiri.com.ar/go/dnss/internal/trace"

	"github.com/miekg/dns"
)

//
// === Tests ===
//

// Test basic functionality.
func TestBasic(t *testing.T) {
	r := testutil.NewTestResolver()

	c := NewCachingResolver(r)

	c.Init()
	if !r.Initialized {
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

// Test the headers and OPT record of cache hit replies.
func TestHitReply(t *testing.T) {
	r := testutil.NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	r.Response = newReply(mustNewRR(t, "test. A 1.2.3.4"))

	tr := trace.New("test", "TestHitReply")
	defer tr.Finish()

	// Plain query: no OPT in the reply.
	req := newQuery("test.", dns.TypeA)
	req.RecursionDesired = false
	c.Query(req, tr)
	resp, err := c.Query(req, tr)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if !statsEquals(2, 1, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}
	if resp.Id != req.Id || !resp.Response || !resp.RecursionAvailable ||
		resp.RecursionDesired || resp.Authoritative ||
		resp.Rcode != dns.RcodeSuccess {
		t.Errorf("bad reply header: %v", resp.MsgHdr)
	}
	if opt := resp.IsEdns0(); opt != nil {
		t.Errorf("unexpected OPT in reply: %v", opt)
	}

	// EDNS0 query with DO: the reply should have an OPT, with DO set.
	req = newQuery("test.", dns.TypeA)
	req.SetEdns0(1232, true)
	c.Query(req, tr)
	resp, err = c.Query(req, tr)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if !statsEquals(4, 2, 2) {
		t.Errorf("bad stats: %v", dumpStats())
	}
	if !resp.RecursionDesired {
		t.Errorf("RD not copied from the request: %v", resp.MsgHdr)
	}
	opt := resp.IsEdns0()
	if opt == nil {
		t.Fatalf("no OPT in reply: %v", resp)
	}
	if opt.UDPSize() != 1232 || !opt.Do() {
		t.Errorf("bad OPT in reply: %v", opt)
	}
}

// Test TTL handling.
// It runs in a synctest bubble, so cache maintenance uses a fake clock and we
// can check the TTLs precisely without waiting.
func TestTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := testutil.NewTestResolver()
		c := NewCachingResolver(r)
		c.Init()
		resetStats()

		// Note we don't start c.Maintain() yet, as we don't want the
		// background TTL updater until later.

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
		// As we've not enabled cache maintenance, we can be sure TTL ==
		// maxTTL.
		resp = queryA(t, c, "", "test.", "1.2.3.4")
		if !statsEquals(2, 1, 1) {
			t.Errorf("bad stats: %v", dumpStats())
		}
		if ttl := getTTL(resp.Answer); ttl != maxTTL {
			t.Errorf("expected max TTL (%v), got %v", maxTTL, ttl)
		}

		// Start cache maintenance, and check that the back resolver's
		// Maintain() is called.
		go c.Maintain(t.Context())
		synctest.Wait()
		select {
		case <-r.MaintainC:
		default:
			t.Errorf("back resolver Maintain() was not called")
		}

		// After each maintenance period, the TTL should be reduced
		// accordingly.
		for i := 1; i <= 3; i++ {
			time.Sleep(maintenancePeriod)
			synctest.Wait()

			resp = queryA(t, c, "", "test.", "1.2.3.4")
			expected := maxTTL - time.Duration(i)*maintenancePeriod
			if ttl := getTTL(resp.Answer); ttl != expected {
				t.Errorf("%d: expected TTL %v, got %v", i, expected, ttl)
			}
		}
	})
}

// Test that requests we can't answer from the cache bypass it.
func TestCacheBypass(t *testing.T) {
	r := testutil.NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	// Populate the cache, so we can check the bypassed requests don't get
	// the cached answer.
	queryA(t, c, "test. A 1.2.3.4", "test.", "1.2.3.4")

	multi := newQuery("test.", dns.TypeA)
	multi.Question = append(multi.Question, multi.Question[0])

	notify := newQuery("test.", dns.TypeA)
	notify.Opcode = dns.OpcodeNotify

	ednsV1 := newQuery("test.", dns.TypeA)
	ednsV1.SetEdns0(4096, false)
	ednsV1.IsEdns0().SetVersion(1)

	ecs := newQuery("test.", dns.TypeA)
	ecs.SetEdns0(4096, false)
	ecs.IsEdns0().Option = append(ecs.IsEdns0().Option, &dns.EDNS0_SUBNET{
		Code:          dns.EDNS0SUBNET,
		Family:        1,
		SourceNetmask: 24,
		Address:       net.ParseIP("192.0.2.0").To4(),
	})

	tr := trace.New("test", "TestCacheBypass")
	defer tr.Finish()

	for _, req := range []*dns.Msg{multi, notify, ednsV1, ecs} {
		resetStats()
		r.LastQuery = nil
		if _, err := c.Query(req, tr); err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if r.LastQuery != req {
			t.Errorf("request was not sent to the back resolver: %v", req)
		}
		if !statsEquals(1, 0, 0) || stats.cacheBypassed.Value() != 1 {
			t.Errorf("bad stats: %v, bypassed %v", dumpStats(),
				stats.cacheBypassed)
		}
	}

	// The answers to the bypassed requests should not have been recorded,
	// and the cache should still have only the original entry.
	if len(c.answer) != 1 {
		t.Errorf("expected 1 entry in the cache, got %d", len(c.answer))
	}
}

// Test that we use the minimum TTL of all the RRs in the answer.
func TestMinTTL(t *testing.T) {
	r := testutil.NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	tr := trace.New("test", "TestMinTTL")
	defer tr.Finish()

	// Long-lived CNAME to a short-lived A: should not be cached, as the A
	// TTL is below minTTL.
	r.Response = newReply(mustNewRR(t, "test. 86400 CNAME cdn.test."))
	r.Response.Answer = append(r.Response.Answer,
		mustNewRR(t, "cdn.test. 30 A 1.2.3.4"))
	req := newQuery("test.", dns.TypeA)
	for range 2 {
		if _, err := c.Query(req, tr); err != nil {
			t.Fatalf("query failed: %v", err)
		}
	}
	if !statsEquals(2, 0, 2) {
		t.Errorf("bad stats: %v", dumpStats())
	}

	// Same, but with an A TTL that is cacheable: all RRs should get it.
	resetStats()
	r.Response = newReply(mustNewRR(t, "test2. 86400 CNAME cdn.test."))
	r.Response.Answer = append(r.Response.Answer,
		mustNewRR(t, "cdn.test. 600 A 1.2.3.4"))
	req = newQuery("test2.", dns.TypeA)
	for range 2 {
		resp, err := c.Query(req, tr)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		for _, rr := range resp.Answer {
			if rr.Header().Ttl != 600 {
				t.Errorf("expected TTL 600, got %v", rr)
			}
		}
	}
	if !statsEquals(2, 1, 1) {
		t.Errorf("bad stats: %v", dumpStats())
	}

	// TTLs with the most significant bit set should be treated as 0, so
	// not cached.
	resetStats()
	r.Response = newReply(mustNewRR(t, "test3. 2147483648 A 1.2.3.4"))
	req = newQuery("test3.", dns.TypeA)
	for range 2 {
		if _, err := c.Query(req, tr); err != nil {
			t.Fatalf("query failed: %v", err)
		}
	}
	if !statsEquals(2, 0, 2) {
		t.Errorf("bad stats: %v", dumpStats())
	}
}

// Test that we don't cache failed queries.
func TestFailedQueries(t *testing.T) {
	r := testutil.NewTestResolver()
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

// Test that queries with different DO and CD bits get separate cache entries.
func TestCacheKeyFlags(t *testing.T) {
	r := testutil.NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	r.Response = newReply(mustNewRR(t, "test. A 1.2.3.4"))

	plain := newQuery("test.", dns.TypeA)
	edns := newQuery("test.", dns.TypeA)
	edns.SetEdns0(4096, false)
	do := newQuery("test.", dns.TypeA)
	do.SetEdns0(4096, true)
	cd := newQuery("test.", dns.TypeA)
	cd.CheckingDisabled = true

	tr := trace.New("test", "TestCacheKeyFlags")
	defer tr.Finish()

	// The first round should be all misses.
	for _, req := range []*dns.Msg{plain, do, cd} {
		if _, err := c.Query(req, tr); err != nil {
			t.Fatalf("query failed: %v", err)
		}
	}
	if !statsEquals(3, 0, 3) {
		t.Errorf("bad stats: %v", dumpStats())
	}

	// The second round should be all hits. EDNS without DO should get the
	// same entry as the plain query.
	for _, req := range []*dns.Msg{plain, edns, do, cd} {
		if _, err := c.Query(req, tr); err != nil {
			t.Fatalf("query failed: %v", err)
		}
	}
	if !statsEquals(7, 4, 3) {
		t.Errorf("bad stats: %v", dumpStats())
	}
}

func TestWantToCache(t *testing.T) {
	query := newQuery("test.", dns.TypeA)
	q := query.Question[0]
	reply := newReply(mustNewRR(t, "test. A 1.2.3.4"))
	reply.Question = []dns.Question{q}

	if err := wantToCache(q, reply); err != nil {
		t.Errorf("wantToCache failed on cacheable request: %v", err)
	}

	r := reply.Copy()
	r.Rcode = dns.RcodeBadName
	checkWantToCache(t, q, r, "unsuccessful query")

	r = reply.Copy()
	r.Response = false
	checkWantToCache(t, q, r, "response = false")

	r = reply.Copy()
	r.Opcode = dns.OpcodeUpdate
	checkWantToCache(t, q, r, "opcode")

	r = reply.Copy()
	r.Answer = []dns.RR{}
	checkWantToCache(t, q, r, "answer is empty")

	r = reply.Copy()
	r.Truncated = true
	checkWantToCache(t, q, r, "truncated reply")

	r = reply.Copy()
	r.Question = []dns.Question{q, q}
	checkWantToCache(t, q, r, "too many/few questions (2)")

	r = reply.Copy()
	r.Question = []dns.Question{}
	checkWantToCache(t, q, r, "too many/few questions (0)")

	r = reply.Copy()
	r.Question = []dns.Question{
		{Name: "other.", Qtype: dns.TypeMX, Qclass: dns.ClassINET}}
	checkWantToCache(t, q, r, "reply question does not match")

	r = reply.Copy()
	r.Answer = nil
	for range 300 {
		r.Answer = append(r.Answer, mustNewRR(t, "test. A 1.2.3.4"))
	}
	checkWantToCache(t, q, r, "answer too large")
}

// Test that we handle the cache filling up, by evicting entries.
func TestCacheFull(t *testing.T) {
	r := testutil.NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	r.Response = newReply(mustNewRR(t, "test. A 1.2.3.4"))

	// Do maxCacheSize+1 different requests.
	for i := 0; i < maxCacheSize+1; i++ {
		queryA(t, c, "", fmt.Sprintf("test%d.", i), "1.2.3.4")
		if !statsEquals(i+1, 0, i+1) {
			t.Errorf("bad stats: %v", dumpStats())
		}
	}

	// The last one should have been recorded by evicting another entry.
	if len(c.answer) != maxCacheSize {
		t.Errorf("expected %d entries, got %d", maxCacheSize, len(c.answer))
	}
	if stats.cacheRecorded.Value() != int64(maxCacheSize+1) ||
		stats.cacheFull.Value() != 1 {
		t.Errorf("expected %d recorded and cache full 1 time, got %v and %v",
			maxCacheSize+1, stats.cacheRecorded, stats.cacheFull)
	}

	// So querying the last one should be a hit.
	resetStats()
	queryA(t, c, "", fmt.Sprintf("test%d.", maxCacheSize), "1.2.3.4")
	if !statsEquals(1, 1, 0) {
		t.Errorf("bad stats: %v", dumpStats())
	}
}

// Test behaviour when the size of the cache is 0 (so users can disable it
// that way).
func TestZeroSize(t *testing.T) {
	r := testutil.NewTestResolver()
	c := NewCachingResolver(r)
	c.Init()
	resetStats()

	// Override the max cache size to 0.
	prevMaxCacheSize := maxCacheSize
	maxCacheSize = 0
	defer func() { maxCacheSize = prevMaxCacheSize }()

	r.Response = newReply(mustNewRR(t, "test. A 1.2.3.4"))

	// Do 5 different requests.
	for i := range 5 {
		queryA(t, c, "", fmt.Sprintf("test%d.", i), "1.2.3.4")
		if !statsEquals(i+1, 0, i+1) {
			t.Errorf("bad stats: %v", dumpStats())
		}
	}

	// Query them back, they should all be misses.
	resetStats()
	for i := range 5 {
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

	r := testutil.NewTestResolver()
	r.Response = newReply(mustNewRR(b, "test. A 1.2.3.4"))

	c := NewCachingResolver(r)
	c.Init()

	tr := trace.New("test", "Benchmark")
	defer tr.Finish()

	req := newQuery("test.", dns.TypeA)

	for b.Loop() {
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
	stats.cacheFull.Set(0)
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
		back := c.back.(*testutil.TestResolver)
		back.Response = newReply(mustNewRR(t, rr))
	}

	tr := trace.New("test", "queryA")
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

	if !slices.Equal(req.Question, resp.Question) {
		t.Errorf("question mis-match: request %v, response %v",
			req.Question, resp.Question)
	}

	return resp
}

func queryFail(t *testing.T, c *cachingResolver) *dns.Msg {
	back := c.back.(*testutil.TestResolver)
	back.Response = &dns.Msg{}
	back.Response.Response = true
	back.Response.Rcode = dns.RcodeNameError

	tr := trace.New("test", "queryFail")
	defer tr.Finish()

	req := newQuery("doesnotexist.", dns.TypeA)
	resp, err := c.Query(req, tr)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	return resp
}

func checkWantToCache(t *testing.T, q dns.Question, r *dns.Msg, exp string) {
	t.Helper()
	err := wantToCache(q, r)
	if !strings.Contains(err.Error(), exp) {
		t.Errorf("q:%v r:%v expected:%q got:%v", q, r, exp, err)
	}
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
