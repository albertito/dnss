// Package httpserver implements an HTTPS server which handles DNS requests
// over HTTPS.
//
// It implements:
//  - Google's DNS over HTTPS using JSON (dns-json), as specified in:
//    https://developers.google.com/speed/public-dns/docs/dns-over-https#api_specification.
//    This is also implemented by Cloudflare's 1.1.1.1, as documented in:
//    https://developers.cloudflare.com/1.1.1.1/dns-over-https/json-format/.
//  - DNS Queries over HTTPS (DoH), as specified in RFC 8484:
//    https://tools.ietf.org/html/rfc8484.
package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"blitiri.com.ar/go/dnss/internal/dnsjson"
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

// Resolve implements the HTTP handler for incoming DNS resolution requests.
// It handles "Google's DNS over HTTPS using JSON" requests, as well as "DoH"
// request.
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

	// Fall back to Google's JSON, the laxer format.
	// It MUST have a "name" query parameter, so we use that for detection.
	if req.Method == "GET" && req.FormValue("name") != "" {
		tr.LazyPrintf("Google-JSON")
		s.resolveJSON(tr, w, req)
		return
	}

	// Could not found how to handle this request.
	util.TraceErrorf(tr, "unknown request type")
	http.Error(w, "unknown request type", http.StatusUnsupportedMediaType)
}

// Resolve "Google's DNS over HTTPS using JSON" requests, and returns
// responses as specified in
// https://developers.google.com/speed/public-dns/docs/dns-over-https#api_specification.
func (s *Server) resolveJSON(tr trace.Trace, w http.ResponseWriter, req *http.Request) {
	// Construct the DNS request from the http query.
	q, err := parseQuery(req.URL)
	if err != nil {
		util.TraceError(tr, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	r := &dns.Msg{}
	r.CheckingDisabled = q.cd
	r.SetQuestion(dns.Fqdn(q.name), q.rrType)

	if q.clientSubnet != nil {
		o := new(dns.OPT)
		o.Hdr.Name = "."
		o.Hdr.Rrtype = dns.TypeOPT
		e := new(dns.EDNS0_SUBNET)
		e.Code = dns.EDNS0SUBNET
		if ipv4 := q.clientSubnet.IP.To4(); ipv4 != nil {
			e.Family = 1 // IPv4 source address
			e.Address = ipv4
		} else {
			e.Family = 2 // IPv6 source address
			e.Address = q.clientSubnet.IP
		}
		e.SourceScope = 0

		_, maskSize := q.clientSubnet.Mask.Size()
		e.SourceNetmask = uint8(maskSize)

		o.Option = append(o.Option, e)
		r.Extra = append(r.Extra, o)
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

	// Convert the reply to json, and write it back.
	jr := &dnsjson.Response{
		Status: fromUp.Rcode,
		TC:     fromUp.Truncated,
		RD:     fromUp.RecursionDesired,
		RA:     fromUp.RecursionAvailable,
		AD:     fromUp.AuthenticatedData,
		CD:     fromUp.CheckingDisabled,
	}

	for _, q := range fromUp.Question {
		rr := dnsjson.RR{
			Name: q.Name,
			Type: q.Qtype,
		}
		jr.Question = append(jr.Question, rr)
	}

	for _, a := range fromUp.Answer {
		hdr := a.Header()
		ja := dnsjson.RR{
			Name: hdr.Name,
			Type: hdr.Rrtype,
			TTL:  hdr.Ttl,
		}

		hs := hdr.String()
		ja.Data = a.String()[len(hs):]
		jr.Answer = append(jr.Answer, ja)
	}

	buf, err := json.Marshal(jr)
	if err != nil {
		err = util.TraceErrorf(tr, "failed to marshal: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(buf)
}

type query struct {
	name   string
	rrType uint16
	cd     bool

	// EDNS client subnet (address+mask).
	clientSubnet *net.IPNet
}

var (
	errEmptyName     = fmt.Errorf("empty name")
	errNameTooLong   = fmt.Errorf("name too long")
	errInvalidSubnet = fmt.Errorf("invalid edns_client_subnet")
	errIntOutOfRange = fmt.Errorf("invalid type (int out of range)")
	errUnknownType   = fmt.Errorf("invalid type (unknown string type)")
	errInvalidCD     = fmt.Errorf("invalid cd value")
)

func parseQuery(u *url.URL) (query, error) {
	q := query{
		name:         "",
		rrType:       1,
		cd:           false,
		clientSubnet: nil,
	}

	// Simplify the values map, as all our parameters are single-value only.
	vs := map[string]string{}
	for k, values := range u.Query() {
		if len(values) > 0 {
			vs[k] = values[0]
		} else {
			vs[k] = ""
		}
	}
	var ok bool
	var err error

	if q.name, ok = vs["name"]; !ok || q.name == "" {
		return q, errEmptyName
	}
	if len(q.name) > 253 {
		return q, errNameTooLong
	}

	if _, ok = vs["type"]; ok {
		q.rrType, err = stringToRRType(vs["type"])
		if err != nil {
			return q, err
		}
	}

	if cd, ok := vs["cd"]; ok {
		q.cd, err = stringToBool(cd)
		if err != nil {
			return q, err
		}
	}

	if clientSubnet, ok := vs["edns_client_subnet"]; ok {
		_, q.clientSubnet, err = net.ParseCIDR(clientSubnet)
		if err != nil {
			return q, errInvalidSubnet
		}
	}

	return q, nil
}

// stringToRRType converts a string into a DNS type constant.
// The string can be a number in the [1, 65535] range, or a canonical type
// string (case-insensitive, such as "A" or "aaaa").
func stringToRRType(s string) (uint16, error) {
	i, err := strconv.ParseInt(s, 10, 16)
	if err == nil {
		if 1 <= i && i <= 65535 {
			return uint16(i), nil
		}
		return 0, errIntOutOfRange
	}

	rrType, ok := dns.StringToType[strings.ToUpper(s)]
	if !ok {
		return 0, errUnknownType
	}
	return rrType, nil
}

func stringToBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "", "1", "true":
		// Note the empty string is intentionally considered true, as long as
		// the parameter is present in the query.
		return true, nil
	case "0", "false":
		return false, nil
	}

	return false, errInvalidCD
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
