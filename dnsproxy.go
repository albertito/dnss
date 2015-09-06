// Generate the protobuf+grpc service.
//go:generate protoc --go_out=plugins=grpc:. dnss.proto

package main

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

func L(w dns.ResponseWriter, r *dns.Msg) string {
	return fmt.Sprintf("%v %v", w.RemoteAddr(), r.Id)
}

func Handler(w dns.ResponseWriter, r *dns.Msg) {
	fmt.Printf("%v %v\n", L(w, r), questionsToString(r.Question))

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.

	from_up, err := dns.Exchange(r, "8.8.8.8:53")
	if err != nil {
		fmt.Printf("%v  ERR: %v\n", L(w, r), err)
		fmt.Printf("%v  UP: %v\n", L(w, r), from_up)
	}

	if from_up != nil {
		if from_up.Rcode != dns.RcodeSuccess {
			rcode := dns.RcodeToString[from_up.Rcode]
			fmt.Printf("%v  !->  %v\n", L(w, r), rcode)

		}
		for _, rr := range from_up.Answer {
			fmt.Printf("%v  ->  %v\n", L(w, r), rr)
		}
		w.WriteMsg(from_up)
	}
}

func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(":5354", "udp", dns.HandlerFunc(Handler))
		fmt.Printf("Exiting UDP: %v\n", err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(":5354", "tcp", dns.HandlerFunc(Handler))
		fmt.Printf("Exiting TCP: %v\n", err)
	}()

	wg.Wait()
}
