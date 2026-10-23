// Package httpserver implements an HTTPS server which handles DNS requests
// over HTTPS.
//
// It implements DNS Queries over HTTPS (DoH), as specified in RFC 8484:
// https://tools.ietf.org/html/rfc8484.
package httpserver

import (
	"encoding/base64"
	"io"
	"mime"
	"net/http"

	"blitiri.com.ar/go/dnss/internal/trace"

	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"
)

// Server is an HTTPS server that implements DNS over HTTPS, see the
// package-level documentation for more references.
type Server struct {
	Addr     string
	Upstream string
	CertFile string
	KeyFile  string
	Insecure bool
}

// ListenAndServe starts the HTTPS server.
func (s *Server) ListenAndServe() {
	srv := http.Server{
		Addr:    s.Addr,
		Handler: s.newMux(),
	}

	log.Infof("HTTPS listening on %s", s.Addr)
	var err error
	if s.Insecure {
		err = srv.ListenAndServe()
	} else {
		err = srv.ListenAndServeTLS(s.CertFile, s.KeyFile)
	}
	log.Fatalf("HTTPS exiting: %s", err)
}

func (s *Server) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	for _, path := range []string{"/dns-query", "/resolve"} {
		mux.HandleFunc("GET "+path, s.resolveGET)
		mux.HandleFunc("POST "+path, s.resolvePOST)
	}
	return mux
}

func newTrace(req *http.Request) *trace.Trace {
	tr := trace.New("httpserver", "/resolve")
	tr.Printf("from:%v", req.RemoteAddr)
	tr.Printf("method:%v", req.Method)
	return tr
}

// Resolve incoming DoH GET requests, which have a "dns=" query parameter.
func (s *Server) resolveGET(w http.ResponseWriter, req *http.Request) {
	tr := newTrace(req)
	defer tr.Finish()

	dnsParam := req.URL.Query().Get("dns")
	if dnsParam == "" {
		tr.Errorf("unknown request type")
		http.Error(w, "unknown request type", http.StatusUnsupportedMediaType)
		return
	}

	tr.Printf("DoH:GET")
	dnsQuery, err := base64.RawURLEncoding.DecodeString(dnsParam)
	if err != nil {
		tr.Error(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.resolveDoH(tr, w, dnsQuery)
}

// Resolve incoming DoH POST requests, which have a content-type of
// application/dns-message.
func (s *Server) resolvePOST(w http.ResponseWriter, req *http.Request) {
	tr := newTrace(req)
	defer tr.Finish()

	ct, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		tr.Error(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if ct != "application/dns-message" {
		tr.Errorf("unknown request type")
		http.Error(w, "unknown request type", http.StatusUnsupportedMediaType)
		return
	}

	tr.Printf("DoH:POST")
	// Limit the size of request to 4k.
	dnsQuery, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 4096))
	if err != nil {
		tr.Error(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.resolveDoH(tr, w, dnsQuery)
}

// Resolve DNS over HTTPS requests, as specified in RFC 8484.
func (s *Server) resolveDoH(tr *trace.Trace, w http.ResponseWriter, dnsQuery []byte) {
	r := &dns.Msg{}
	err := r.Unpack(dnsQuery)
	if err != nil {
		tr.Error(err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tr.Question(r.Question)

	// Do the DNS request, get the reply.
	fromUp, err := exchange(tr, r, s.Upstream)
	if err != nil {
		err = tr.Errorf("dns exchange error: %v", err)
		http.Error(w, err.Error(), http.StatusFailedDependency)
		return
	}

	if fromUp == nil {
		err = tr.Errorf("no response from upstream")
		http.Error(w, err.Error(), http.StatusRequestTimeout)
		return
	}

	tr.Answer(fromUp)

	// Compress the reply. Messages are unpacked without compression, and
	// packing them back as-is can make them much larger, even beyond the
	// DoH limit of 64 KiB.
	fromUp.Compress = true

	packed, err := fromUp.Pack()
	if err != nil {
		err = tr.Errorf("cannot pack reply: %v", err)
		http.Error(w, err.Error(), http.StatusFailedDependency)
		return
	}

	// Write the response back.
	w.Header().Set("Content-type", "application/dns-message")
	// TODO: set cache-control based on the response.
	w.WriteHeader(http.StatusOK)
	w.Write(packed)
}

func exchange(tr *trace.Trace, r *dns.Msg, addr string) (*dns.Msg, error) {
	reply, err := dns.Exchange(r, addr)
	if err == nil && !reply.Truncated {
		tr.Printf("UDP exchange successful")
		return reply, err
	}

	// If we had issues over UDP, or the message was truncated, retry over
	// TCP. We don't try beyond that.
	if err != nil {
		tr.Printf("error on UDP exchange: %v", err)
	} else if reply.Truncated {
		tr.Printf("UDP exchange returned truncated reply: %v", reply.MsgHdr)
	}
	tr.Printf("retrying on TCP")

	c := &dns.Client{
		Net: "tcp",
	}

	reply, _, err = c.Exchange(r, addr)
	return reply, err
}
