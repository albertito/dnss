package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blitiri.com.ar/go/dnss/internal/testutil"
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
		"/ignored?dns=q80BAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("good test: expected http status ok, got %v",
			resp.StatusCode)
	}

	// Invalid request (error unpacking)
	resp = query(t, srv, "GET",
		"/ignored?dns=0000", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unpacking test: expected bad request, got %v",
			resp.StatusCode)
	}

	// Error reading request body.
	{
		req := httptest.NewRequest("POST", "/ignored", nil)
		req.Header.Set("Content-Type", "application/dns-message")
		req.Body = errorReadCloser{}
		w := httptest.NewRecorder()
		srv.Resolve(w, req)
		resp := w.Result()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("bad body test: expected bad request, got %v",
				resp.StatusCode)
		}
	}

	// Upstream error.
	// Put this last because we override the upstream address.
	srv.Upstream = "localhost:0"
	resp = query(t, srv, "GET",
		"/ignored?dns=q80BAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB", "")
	if resp.StatusCode != http.StatusFailedDependency {
		t.Errorf("bad upstream test: expected failed dependency, got %v",
			resp.StatusCode)
	}
}

func query(t *testing.T, srv *Server, method, url, body string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")

	w := httptest.NewRecorder()
	srv.Resolve(w, req)

	return w.Result()
}

type errorReadCloser struct{}

func (errorReadCloser) Read(p []byte) (int, error) {
	return 0, errors.New("error for testing")
}
func (errorReadCloser) Close() error { return nil }
