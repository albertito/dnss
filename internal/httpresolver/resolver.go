package httpresolver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"blitiri.com.ar/go/dnss/internal/dnsserver"
	"blitiri.com.ar/go/dnss/internal/trace"

	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"
)

// httpsResolver implements the dnsserver.Resolver interface by querying a
// server via DNS over HTTPS (DoH, RFC 8484).
type httpsResolver struct {
	Upstream *url.URL
	CAFile   string

	// net.Resolver that will contact the server at --fallback_upstream for
	// DNS resolutions.
	fallbackResolver *net.Resolver

	client *http.Client

	// Trace to keep track of client errors, for debugging.
	tr *trace.Trace
}

var errAppendingCerts = errors.New("error appending certificates")

func loadCertPool(caFile string) (*x509.CertPool, error) {
	pemData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errAppendingCerts
	}

	return pool, nil
}

// NewDoH creates a new DoH resolver, which uses the given upstream
// URL to resolve queries.
func NewDoH(upstream *url.URL, caFile, fallback string) *httpsResolver {
	r := &httpsResolver{
		Upstream: upstream,
		CAFile:   caFile,
	}

	if fallback != "" {
		// Dial function that will always use the fallback address to contact
		// DNS.
		dialer := net.Dialer{}
		dialFallback := func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, fallback)
		}

		r.fallbackResolver = &net.Resolver{
			PreferGo: true, // Avoid the system resolver.
			Dial:     dialFallback,
		}
	}

	return r
}

func (r *httpsResolver) Init() error {
	// If CAFile is empty, we're ok with the defaults (use the system default
	// CA database).
	var tlsConfig *tls.Config
	if r.CAFile != "" {
		pool, err := loadCertPool(r.CAFile)
		if err != nil {
			return err
		}

		tlsConfig = &tls.Config{
			RootCAs: pool,
		}
	}

	r.client = newClient(tlsConfig, r.fallbackResolver)
	r.tr = trace.New("httpresolver.Client", r.Upstream.String())
	r.tr.Printf("Init complete")

	return nil
}

func newClient(tlsConfig *tls.Config, resolver *net.Resolver) *http.Client {
	// Start from the default transport, which has reasonable settings (e.g.
	// for timeouts, and taking proxy settings from the environment).
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	// Use our resolver to find the upstream's address.
	// Limit the dial timeout, as queries time out sooner anyway. Note dials
	// can outlive the queries that started them, so the connection may still
	// be useful for later queries.
	transport.DialContext = (&net.Dialer{
		Timeout:  10 * time.Second,
		Resolver: resolver,
	}).DialContext

	// Enable HTTP/2 health checks: if we haven't received anything on a
	// connection for 5s, send a ping, and close the connection if there is no
	// response within 3s.
	// This detects dead connections (e.g. when the network changes, or after
	// an outage), which otherwise the transport would keep using for a long
	// time.
	transport.HTTP2 = &http.HTTP2Config{
		SendPingTimeout: 5 * time.Second,
		PingTimeout:     3 * time.Second,
	}

	return &http.Client{
		// Give our HTTP requests 4 second timeouts: DNS usually doesn't wait
		// that long anyway, but this helps with slow connections.
		Timeout: 4 * time.Second,

		Transport: transport,
	}
}

// Maintain the resolver. There's nothing to do, as the HTTP transport takes
// care of the connections.
func (r *httpsResolver) Maintain(ctx context.Context) {}

func (r *httpsResolver) Query(req *dns.Msg, tr *trace.Trace) (*dns.Msg, error) {
	packed, err := req.Pack()
	if err != nil {
		return nil, fmt.Errorf("cannot pack query: %w", err)
	}

	if log.V(1) {
		tr.Printf("DoH POST %v", r.Upstream)
	}

	// TODO: Accept header.

	hr, err := r.client.Post(
		r.Upstream.String(),
		"application/dns-message",
		bytes.NewReader(packed))
	if err != nil {
		r.tr.Printf("Client error: %v", err)
		return nil, fmt.Errorf("POST failed: %w", err)
	}
	tr.Printf("%s  %s", hr.Proto, hr.Status)
	defer hr.Body.Close()

	if hr.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Response status: %s", hr.Status)
	}

	// Read the HTTPS response, and parse the message.
	ct, _, err := mime.ParseMediaType(hr.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse content type: %w", err)
	}

	if ct != "application/dns-message" {
		return nil, fmt.Errorf("unknown response content type %q", ct)
	}

	respRaw, err := io.ReadAll(io.LimitReader(hr.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("error reading from body: %w", err)
	}

	respDNS := &dns.Msg{}
	err = respDNS.Unpack(respRaw)
	if err != nil {
		return nil, fmt.Errorf("error unpacking response: %w", err)
	}

	return respDNS, nil
}

// Compile-time check that the implementation matches the interface.
var _ dnsserver.Resolver = &httpsResolver{}
