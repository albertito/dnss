// GRPC to DNS.

package grpctodns

import (
	"fmt"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

func questionsToString(qs []dns.Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, fmt.Sprintf("(%s %s %s)", q.Name,
			dns.TypeToString[q.Qtype], dns.ClassToString[q.Qclass]))
	}
	return "Q[" + strings.Join(s, " ") + "]"
}

func rrsToString(rrs []dns.RR) string {
	var s []string
	for _, rr := range rrs {
		s = append(s, fmt.Sprintf("(%s)", rr))
	}
	return "RR[" + strings.Join(s, " ") + "]"

}

func l(w dns.ResponseWriter, r *dns.Msg) string {
	return fmt.Sprintf("%v %v", w.RemoteAddr(), r.Id)
}

type Server struct {
	Addr     string
	Upstream string
}

func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	fmt.Printf("GRPC %v %v\n", l(w, r), questionsToString(r.Question))

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.

	from_up, err := dns.Exchange(r, s.Upstream)
	if err != nil {
		fmt.Printf("GRPC %v  ERR: %v\n", l(w, r), err)
		fmt.Printf("GRPC %v  UP: %v\n", l(w, r), from_up)
	}

	if from_up != nil {
		if from_up.Rcode != dns.RcodeSuccess {
			rcode := dns.RcodeToString[from_up.Rcode]
			fmt.Printf("GPRC %v  !->  %v\n", l(w, r), rcode)
		}
		for _, rr := range from_up.Answer {
			fmt.Printf("GRPC %v  ->  %v\n", l(w, r), rr)
		}
		w.WriteMsg(from_up)
	}
}

func (s *Server) ListenAndServe() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "udp", dns.HandlerFunc(s.Handler))
		fmt.Printf("Exiting UDP: %v\n", err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "tcp", dns.HandlerFunc(s.Handler))
		fmt.Printf("Exiting TCP: %v\n", err)
	}()

	wg.Wait()
}
