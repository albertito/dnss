package util

import (
	"fmt"
	"strings"

	"blitiri.com.ar/go/log"

	"github.com/miekg/dns"
	"golang.org/x/net/trace"
)

// TraceQuestion adds the given question to the trace.
func TraceQuestion(tr trace.Trace, qs []dns.Question) {
	if !log.V(3) {
		return
	}

	tr.LazyPrintf(questionsToString(qs))
}

func questionsToString(qs []dns.Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, fmt.Sprintf("(%s %s %s)", q.Name,
			dns.TypeToString[q.Qtype], dns.ClassToString[q.Qclass]))
	}
	return "Q: " + strings.Join(s, " ; ")
}

// TraceAnswer adds the given DNS answer to the trace.
func TraceAnswer(tr trace.Trace, m *dns.Msg) {
	if !log.V(3) {
		return
	}

	tr.LazyPrintf(m.MsgHdr.String())
	for _, rr := range m.Answer {
		tr.LazyPrintf(rr.String())
	}
}

// TraceError adds the given error to the trace.
func TraceError(tr trace.Trace, err error) {
	log.Infof(err.Error())
	tr.LazyPrintf(err.Error())
	tr.SetError()
}

// TraceErrorf adds an error message to the trace.
func TraceErrorf(tr trace.Trace, format string, a ...interface{}) error {
	err := fmt.Errorf(format, a...)
	TraceError(tr, err)
	return err
}
