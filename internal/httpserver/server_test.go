package httpserver

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blitiri.com.ar/go/dnss/internal/testutil"

	"github.com/miekg/dns"
)

func TestBasic(t *testing.T) {
	upstreamAddr := testutil.GetFreePort()
	go testutil.ServeTestDNSServer(upstreamAddr,
		testutil.MakeStaticHandler(t, "test. A 1.1.1.1"))
	testutil.WaitForDNSServer(upstreamAddr)

	srv := &Server{
		Upstream: upstreamAddr,
	}

	// Simple successful query.
	resp := query(t, srv, "GET",
		"/dns-query?dns=q80BAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("good test: expected http status ok, got %v",
			resp.StatusCode)
	}

	// Invalid request (error unpacking)
	resp = query(t, srv, "GET",
		"/dns-query?dns=0000", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unpacking test: expected bad request, got %v",
			resp.StatusCode)
	}

	// Error reading request body.
	{
		req := httptest.NewRequest("POST", "/dns-query", nil)
		req.Header.Set("Content-Type", "application/dns-message")
		req.Body = errorReadCloser{}
		w := httptest.NewRecorder()
		srv.newMux().ServeHTTP(w, req)
		resp := w.Result()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("bad body test: expected bad request, got %v",
				resp.StatusCode)
		}
	}

	// Requests we don't know how to handle.
	resp = query(t, srv, "GET", "/dns-query", "")
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("GET without dns: expected unsupported media type, got %v",
			resp.StatusCode)
	}
	resp = query(t, srv, "PUT", "/dns-query", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("PUT: expected method not allowed, got %v",
			resp.StatusCode)
	}

	// Upstream error.
	// Put this last because we override the upstream address.
	srv.Upstream = "localhost:0"
	resp = query(t, srv, "GET",
		"/dns-query?dns=q80BAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB", "")
	if resp.StatusCode != http.StatusFailedDependency {
		t.Errorf("bad upstream test: expected failed dependency, got %v",
			resp.StatusCode)
	}
}

// Test that large answers, which only fit when compressed, are delivered.
func TestLargeAnswer(t *testing.T) {
	upstreamAddr := testutil.GetFreePort()
	go testutil.ServeTestDNSServer(upstreamAddr,
		func(w dns.ResponseWriter, r *dns.Msg) {
			m := &dns.Msg{}
			m.SetReply(r)
			if r.Question[0].Name == testutil.LargeAnswerName {
				m.Answer = testutil.NewLargeAnswer(300)
			}
			m.Compress = true
			w.WriteMsg(m)
		})
	testutil.WaitForDNSServer(upstreamAddr)

	srv := &Server{
		Upstream: upstreamAddr,
	}

	// Use EDNS0 so the upstream query can get the whole answer over UDP,
	// as our test server doesn't support TCP.
	m := &dns.Msg{}
	m.SetQuestion(testutil.LargeAnswerName, dns.TypeA)
	m.SetEdns0(dns.MaxMsgSize, false)
	packed, err := m.Pack()
	if err != nil {
		t.Fatalf("error packing query: %v", err)
	}

	resp := query(t, srv, "POST", "/dns-query", string(packed))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected http status ok, got %v", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("error reading body: %v", err)
	}
	if len(body) > dns.MaxMsgSize {
		t.Errorf("reply is too large: %d bytes", len(body))
	}

	reply := &dns.Msg{}
	if err := reply.Unpack(body); err != nil {
		t.Fatalf("error unpacking reply: %v", err)
	}
	if len(reply.Answer) != 300 {
		t.Errorf("expected 300 RRs, got %d", len(reply.Answer))
	}
}

func query(t *testing.T, srv *Server, method, url, body string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")

	w := httptest.NewRecorder()
	srv.newMux().ServeHTTP(w, req)

	return w.Result()
}

type errorReadCloser struct{}

func (errorReadCloser) Read(p []byte) (int, error) {
	return 0, errors.New("error for testing")
}
func (errorReadCloser) Close() error { return nil }
