// Package httpserver implements an HTTPS server which handles DNS requests
// over HTTPS.
//
// It implements DNS Queries over HTTPS (DoH), as specified in RFC 8484:
// https://tools.ietf.org/html/rfc8484.
package httpserver

import (
	"encoding/base64"
	"io"
	"io/ioutil"
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
	mux := http.NewServeMux()
	mux.HandleFunc("/dns-query", s.Resolve)
	mux.HandleFunc("/resolve", s.Resolve)
	srv := http.Server{
		Addr:    s.Addr,
		Handler: mux,
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

// Resolve incoming DoH requests.
func (s *Server) Resolve(w http.ResponseWriter, req *http.Request) {
	tr := trace.New("httpserver", "/resolve")
	defer tr.Finish()
	tr.Printf("from:%v", req.RemoteAddr)
	tr.Printf("method:%v", req.Method)

	req.ParseForm()

	// Identify DoH requests:
	//  - GET requests have a "dns=" query parameter.
	//  - POST requests have a content-type = application/dns-message.
	if req.Method == "GET" && req.FormValue("dns") != "" {
		tr.Printf("DoH:GET")
		dnsQuery, err := base64.RawURLEncoding.DecodeString(
			req.FormValue("dns"))
		if err != nil {
			tr.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.resolveDoH(tr, w, dnsQuery)
		return
	}

	if req.Method == "POST" {
		ct, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil {
			tr.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if ct == "application/dns-message" {
			tr.Printf("DoH:POST")
			// Limit the size of request to 4k.
			dnsQuery, err := ioutil.ReadAll(io.LimitReader(req.Body, 4092))
			if err != nil {
				tr.Error(err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			s.resolveDoH(tr, w, dnsQuery)
			return
		}
	}

	// Could not found how to handle this request.
	tr.Errorf("unknown request type")
	http.Error(w, "unknown request type", http.StatusUnsupportedMediaType)
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
	fromUp, err := dns.Exchange(r, s.Upstream)
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
