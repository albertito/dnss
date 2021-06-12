package httpresolver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"blitiri.com.ar/go/dnss/internal/dnsserver"
	"blitiri.com.ar/go/dnss/internal/trace"

	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"
)

// httpsResolver implements the dnsserver.Resolver interface by querying a
// server via DNS over HTTPS (DoH, RFC 8484).
type httpsResolver struct {
	Upstream  *url.URL
	CAFile    string
	tlsConfig *tls.Config

	// net.Resolver that will contact the server at --fallback_upstream for
	// DNS resolutions.
	fallbackResolver *net.Resolver

	mu       sync.Mutex
	client   *http.Client
	firstErr time.Time
}

var errAppendingCerts = fmt.Errorf("error appending certificates")

func loadCertPool(caFile string) (*x509.CertPool, error) {
	pemData, err := ioutil.ReadFile(caFile)
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
	if r.CAFile != "" {
		pool, err := loadCertPool(r.CAFile)
		if err != nil {
			return err
		}

		r.tlsConfig = &tls.Config{
			RootCAs: pool,
		}
	}

	client, err := r.newClient()

	r.mu.Lock()
	r.client = client
	r.mu.Unlock()

	tr := trace.New("httpresolver", r.Upstream.String())
	tr.Printf("Init complete, client: %p", r.client)
	tr.Finish()

	return err
}

func (r *httpsResolver) newClient() (*http.Client, error) {
	transport := &http.Transport{
		TLSClientConfig: r.tlsConfig,

		// Take the semi-standard proxy settings from the environment.
		Proxy: http.ProxyFromEnvironment,

		// Drop connections after 30s idle.
		// This helps prevent connection pile-up on frequent client rotations,
		// which can happen with intermittent network issues.
		IdleConnTimeout: 30 * time.Second,

		// Reasonable defaults, based on http.DefaultTransport.
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 1 * time.Second,
			DualStack: true,
			Resolver:  r.fallbackResolver,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		TLSHandshakeTimeout:   4 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		// Give our HTTP requests 4 second timeouts: DNS usually doesn't wait
		// that long anyway, but this helps with slow connections.
		Timeout: 4 * time.Second,

		Transport: transport,
	}

	return client, nil
}

func (r *httpsResolver) setClientError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err == nil {
		r.firstErr = time.Time{}
	} else if r.firstErr.IsZero() {
		r.firstErr = time.Now()
	}
}

func (r *httpsResolver) Maintain() {
	for range time.Tick(2 * time.Second) {
		r.maybeRotateClient()
	}
}

func (r *httpsResolver) maybeRotateClient() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.firstErr.IsZero() {
		return
	}

	// If we've seen errors for the last 10s, rotate the client.
	// This is unfortunately needed because the Go HTTP/2 transport will
	// insist on using a dead connection for a long time, and cannot be told
	// to close it. This causes problems when the computer changes connections
	// (e.g. switch wifi network) or is having intermittent network issues.
	// This workaround works because a new client will initiate a new
	// connection, and the old one will die in the background.
	// The time chosen here combines with the transport timeouts set above, so
	// we never have too many in-flight connections.
	if time.Since(r.firstErr) > 10*time.Second {
		tr := trace.New("httpresolver", r.Upstream.String())
		defer tr.Finish()

		tr.Printf("Rotating client after %s of errors: %p",
			time.Since(r.firstErr), r.client)
		client, err := r.newClient()
		if err != nil {
			tr.Errorf("Error creating new client: %v", err)
			return
		}

		r.client = client
		r.firstErr = time.Time{}
		tr.Printf("Rotated client: %p", r.client)
	}
}

func (r *httpsResolver) Query(req *dns.Msg, tr *trace.Trace) (*dns.Msg, error) {
	packed, err := req.Pack()
	if err != nil {
		return nil, fmt.Errorf("cannot pack query: %v", err)
	}

	if log.V(3) {
		tr.Printf("DoH POST %v", r.Upstream)
	}

	// TODO: Accept header.

	r.mu.Lock()
	client := r.client
	r.mu.Unlock()

	hr, err := client.Post(
		r.Upstream.String(),
		"application/dns-message",
		bytes.NewReader(packed))
	r.setClientError(err)
	if err != nil {
		return nil, fmt.Errorf("POST failed: %v", err)
	}
	tr.Printf("%s  %s", hr.Proto, hr.Status)
	defer hr.Body.Close()

	if hr.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Response status: %s", hr.Status)
	}

	// Read the HTTPS response, and parse the message.
	ct, _, err := mime.ParseMediaType(hr.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse content type: %v", err)
	}

	if ct != "application/dns-message" {
		return nil, fmt.Errorf("unknown response content type %q", ct)
	}

	respRaw, err := ioutil.ReadAll(io.LimitReader(hr.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("error reading from body: %v", err)
	}

	respDNS := &dns.Msg{}
	err = respDNS.Unpack(respRaw)
	if err != nil {
		return nil, fmt.Errorf("error unpacking response: %v", err)
	}

	return respDNS, nil
}

// Compile-time check that the implementation matches the interface.
var _ dnsserver.Resolver = &httpsResolver{}
