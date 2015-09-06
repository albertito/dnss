package util

// Utility functions for printing DNS messages.

import (
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

func QuestionsToString(qs []dns.Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, fmt.Sprintf("(%s %s %s)", q.Name,
			dns.TypeToString[q.Qtype], dns.ClassToString[q.Qclass]))
	}
	return "Q[" + strings.Join(s, " ") + "]"
}
