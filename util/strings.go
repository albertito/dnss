package util

// Utility functions for logging DNS messages.

import (
	"fmt"
	"strings"

	"github.com/miekg/dns"
	"golang.org/x/net/trace"
)

func QuestionsToString(qs []dns.Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, fmt.Sprintf("(%s %s %s)", q.Name,
			dns.TypeToString[q.Qtype], dns.ClassToString[q.Qclass]))
	}
	return "Q: " + strings.Join(s, " ; ")
}

func TraceAnswer(tr trace.Trace, m *dns.Msg) {
	if m.Rcode != dns.RcodeSuccess {
		rcode := dns.RcodeToString[m.Rcode]
		tr.LazyPrintf(rcode)
	}
	for _, rr := range m.Answer {
		tr.LazyPrintf(rr.String())
	}
}
