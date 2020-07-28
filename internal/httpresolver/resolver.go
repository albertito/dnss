package httpresolver

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"
	"time"

	"blitiri.com.ar/go/dnss/internal/dnsserver"
	"blitiri.com.ar/go/log"

	"github.com/miekg/dns"
	"golang.org/x/net/trace"
)

// httpsResolver implements the dnsserver.Resolver interface by querying a
// server via DNS over HTTPS (DoH, RFC 8484).
type httpsResolver struct {
	Upstream *url.URL
	CAFile   string
	client   *http.Client
}

func loadCertPool(caFile string) (*x509.CertPool, error) {
	pemData, err := ioutil.ReadFile(caFile)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("Error appending certificates")
	}

	return pool, nil
}

// NewDoH creates a new DoH resolver, which uses the given upstream
// URL to resolve queries.
func NewDoH(upstream *url.URL, caFile string) *httpsResolver {
	return &httpsResolver{
		Upstream: upstream,
		CAFile:   caFile,
	}
}

func (r *httpsResolver) Init() error {
	transport := &http.Transport{
		// Take the semi-standard proxy settings from the environment.
		Proxy: http.ProxyFromEnvironment,
	}

	r.client = &http.Client{
		// Give our HTTP requests 4 second timeouts: DNS usually doesn't wait
		// that long anyway, but this helps with slow connections.
		Timeout: 4 * time.Second,

		Transport: transport,
	}

	// If CAFile is empty, we're ok with the defaults (use the system default
	// CA database).
	if r.CAFile != "" {
		pool, err := loadCertPool(r.CAFile)
		if err != nil {
			return err
		}

		transport.TLSClientConfig = &tls.Config{
			ClientCAs: pool,
		}
	}

	return nil
}

func (r *httpsResolver) Maintain() {
}

func (r *httpsResolver) Query(req *dns.Msg, tr trace.Trace) (*dns.Msg, error) {
	packed, err := req.Pack()
	if err != nil {
		return nil, fmt.Errorf("cannot pack query: %v", err)
	}

	if log.V(3) {
		tr.LazyPrintf("DoH POST %v", r.Upstream)
	}

	// TODO: Accept header.

	hr, err := r.client.Post(
		r.Upstream.String(),
		"application/dns-message",
		bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("POST failed: %v", err)
	}
	tr.LazyPrintf("%s  %s", hr.Proto, hr.Status)
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
