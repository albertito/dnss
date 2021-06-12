package httpresolver

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"blitiri.com.ar/go/dnss/internal/testutil"
	"blitiri.com.ar/go/dnss/internal/trace"
	"github.com/miekg/dns"
)

//////////////////////////////////////////////////////////////////
// Tests for the Query handler.

func mustNewDoH(t *testing.T, urlS string) *httpsResolver {
	t.Helper()

	u, err := url.Parse(urlS)
	if err != nil {
		t.Errorf("Error building URL from %q: %s", urlS, err)
	}

	r := NewDoH(u, "", "0.0.0.0:0")

	err = r.Init()
	if err != nil {
		t.Errorf("Init() failed: %v", err)
	}

	return r
}

func query(t *testing.T, r *httpsResolver, req string) (dns.RR, error) {
	t.Helper()
	tr := trace.New("test", "query")
	defer tr.Finish()

	dr := new(dns.Msg)
	dr.SetQuestion(req, dns.TypeA)
	resp, err := r.Query(dr, tr)
	if resp != nil && resp.Answer != nil && len(resp.Answer) == 1 {
		return resp.Answer[0], err
	}
	return nil, err
}

func queryExpectA(t *testing.T, r *httpsResolver, req, expectedA string) {
	t.Helper()
	ans, err := query(t, r, req)
	if err != nil {
		t.Fatalf("Query returned error: %v", err)
	}
	if ip := ans.(*dns.A).A; !ip.Equal(net.ParseIP(expectedA)) {
		t.Errorf("Expected answer %s, got %v", expectedA, ip)
	}
}

func queryExpectErr(t *testing.T, r *httpsResolver, req, errContains string) {
	t.Helper()
	_, err := query(t, r, req)
	if !strings.Contains(err.Error(), errContains) {
		t.Errorf("Expected error to contain %q, got %q", errContains, err)
	}
}

func TestBasic(t *testing.T) {
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dns-message")
			m := &dns.Msg{}
			m.Answer = append(m.Answer,
				testutil.NewRR(t, "test.blah. A 1.2.3.4"))
			msg, err := m.Pack()
			if err != nil {
				t.Fatalf("Error packing reply: %v", err)
			}
			w.Write(msg)
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	queryExpectA(t, r, "test.blah.", "1.2.3.4")
}

func TestInvalidServer(t *testing.T) {
	r := mustNewDoH(t, "http://0.0.0.0/")
	queryExpectErr(t, r, "test.blah.", "POST failed:")
}

func TestNotOK(t *testing.T) {
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Something is broken", http.StatusTeapot)
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	queryExpectErr(t, r, "test.blah.", "Response status:")
}

func TestNoContentType(t *testing.T) {
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	queryExpectErr(t, r, "test.blah.", "failed to parse content type:")
}

func TestWrongContentType(t *testing.T) {
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "cat/cat")
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	queryExpectErr(t, r, "test.blah.", "unknown response content type")
}

func TestNoBody(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dns-message")
			// Write some data so it gets flushed to the client before
			// abruptly closing the connection.
			for i := 0; i < 2000; i++ {
				fmt.Fprintf(w, "some response\n")
			}
			defer ts.CloseClientConnections()
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	queryExpectErr(t, r, "test.blah.", "error reading from body")
}

func TestBadBody(t *testing.T) {
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dns-message")
			fmt.Fprintf(w, "this is not a DNS reply\n")
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	queryExpectErr(t, r, "test.blah.", "error unpacking response")
}

func TestBadRequest(t *testing.T) {
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/dns-message")
			fmt.Fprintf(w, "Should not get to run this\n")
		}))
	defer ts.Close()

	r := mustNewDoH(t, ts.URL)
	tr := trace.New("test", "TestBadRequest")
	defer tr.Finish()

	// Construct a request that cannot be packed, in this case the Rcode is
	// invalid.
	dr := new(dns.Msg)
	dr.SetQuestion("test.blah.", dns.TypeA)
	dr.Rcode = -1
	_, err := r.Query(dr, tr)
	if !strings.Contains(err.Error(), "cannot pack query") {
		t.Errorf("Expected error to contain 'cannot pack query', got %q", err)
	}
}

//////////////////////////////////////////////////////////////////
// Tests for the helper functions.

func TestBadCertPools(t *testing.T) {
	r := &httpsResolver{CAFile: "/doesnotexist"}
	err := r.Init()
	if !os.IsNotExist(err) {
		t.Errorf("load non-existing file, got: %v", err)
	}

	// Load a file which doesn't have proper contents.
	r = &httpsResolver{CAFile: "resolver_test.go"}
	err = r.Init()
	if err != errAppendingCerts {
		t.Errorf("invalid cert file, got: %v", err)
	}

	// Valid cases get exercised on the integration tests.
}
