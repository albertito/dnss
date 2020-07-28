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

	"blitiri.com.ar/go/dnss/internal/util"
	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"
	"golang.org/x/net/trace"
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
	tr.LazyPrintf("from:%v", req.RemoteAddr)
	tr.LazyPrintf("method:%v", req.Method)

	req.ParseForm()

	// Identify DoH requests:
	//  - GET requests have a "dns=" query parameter.
	//  - POST requests have a content-type = application/dns-message.
	if req.Method == "GET" && req.FormValue("dns") != "" {
		tr.LazyPrintf("DoH:GET")
		dnsQuery, err := base64.RawURLEncoding.DecodeString(
			req.FormValue("dns"))
		if err != nil {
			util.TraceError(tr, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.resolveDoH(tr, w, dnsQuery)
		return
	}

	if req.Method == "POST" {
		ct, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil {
			util.TraceError(tr, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if ct == "application/dns-message" {
			tr.LazyPrintf("DoH:POST")
			// Limit the size of request to 4k.
			dnsQuery, err := ioutil.ReadAll(io.LimitReader(req.Body, 4092))
			if err != nil {
				util.TraceError(tr, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			s.resolveDoH(tr, w, dnsQuery)
			return
		}
	}

	// Could not found how to handle this request.
	util.TraceErrorf(tr, "unknown request type")
	http.Error(w, "unknown request type", http.StatusUnsupportedMediaType)
}

// Resolve DNS over HTTPS requests, as specified in RFC 8484.
func (s *Server) resolveDoH(tr trace.Trace, w http.ResponseWriter, dnsQuery []byte) {
	r := &dns.Msg{}
	err := r.Unpack(dnsQuery)
	if err != nil {
		util.TraceError(tr, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	util.TraceQuestion(tr, r.Question)

	// Do the DNS request, get the reply.
	fromUp, err := dns.Exchange(r, s.Upstream)
	if err != nil {
		err = util.TraceErrorf(tr, "dns exchange error: %v", err)
		http.Error(w, err.Error(), http.StatusFailedDependency)
		return
	}

	if fromUp == nil {
		err = util.TraceErrorf(tr, "no response from upstream")
		http.Error(w, err.Error(), http.StatusRequestTimeout)
		return
	}

	util.TraceAnswer(tr, fromUp)

	packed, err := fromUp.Pack()
	if err != nil {
		err = util.TraceErrorf(tr, "cannot pack reply: %v", err)
		http.Error(w, err.Error(), http.StatusFailedDependency)
		return
	}

	// Write the response back.
	w.Header().Set("Content-type", "application/dns-message")
	// TODO: set cache-control based on the response.
	w.WriteHeader(http.StatusOK)
	w.Write(packed)
}
