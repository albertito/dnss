// Package trace extends golang.org/x/net/trace.
package trace

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"blitiri.com.ar/go/log"
	"github.com/miekg/dns"

	nettrace "golang.org/x/net/trace"
)

func init() {
	// golang.org/x/net/trace has its own authorization which by default only
	// allows localhost. This can be confusing and limiting in environments
	// which access the monitoring server remotely.
	nettrace.AuthRequest = func(req *http.Request) (any, sensitive bool) {
		return true, true
	}
}

// A Trace represents an active request.
type Trace struct {
	family string
	title  string
	t      nettrace.Trace
}

// New trace.
func New(family, title string) *Trace {
	t := &Trace{family, title, nettrace.New(family, title)}

	// The default for max events is 10, which is a bit short for our uses.
	// Expand it to 30 which should be large enough to keep most of the
	// traces.
	t.t.SetMaxEvents(30)
	return t
}

// Printf adds this message to the trace's log.
func (t *Trace) Printf(format string, a ...interface{}) {
	t.printf(1, format, a...)
}

func (t *Trace) printf(n int, format string, a ...interface{}) {
	t.t.LazyPrintf(format, a...)

	log.Log(log.Debug, n+1, "%s %s: %s", t.family, t.title,
		quote(fmt.Sprintf(format, a...)))
}

// Errorf adds this message to the trace's log, with an error level.
func (t *Trace) Errorf(format string, a ...interface{}) error {
	// Note we can't just call t.Error here, as it breaks caller logging.
	err := fmt.Errorf(format, a...)
	t.t.SetError()
	t.t.LazyPrintf("error: %v", err)

	log.Log(log.Info, 1, "%s %s: error: %s", t.family, t.title,
		quote(err.Error()))
	return err
}

// Error marks the trace as having seen an error, and also logs it to the
// trace's log.
func (t *Trace) Error(err error) error {
	t.t.SetError()
	t.t.LazyPrintf("error: %v", err)

	log.Log(log.Info, 1, "%s %s: error: %s", t.family, t.title,
		quote(err.Error()))

	return err
}

// Finish the trace. It should not be changed after this is called.
func (t *Trace) Finish() {
	t.t.Finish()
}

////////////////////////////////////////////////////////////
// DNS specific extensions
//

// Question adds the given question to the trace.
func (t *Trace) Question(qs []dns.Question) {
	if !log.V(3) {
		return
	}

	t.printf(1, questionsToString(qs))
}

func questionsToString(qs []dns.Question) string {
	var s []string
	for _, q := range qs {
		s = append(s, fmt.Sprintf("(%s %s %s)", q.Name,
			dns.TypeToString[q.Qtype], dns.ClassToString[q.Qclass]))
	}
	return "Q: " + strings.Join(s, " ; ")
}

// Answer adds the given DNS answer to the trace.
func (t *Trace) Answer(m *dns.Msg) {
	if !log.V(3) {
		return
	}

	t.printf(1, m.MsgHdr.String())
	for _, rr := range m.Answer {
		t.printf(1, rr.String())
	}
}

func quote(s string) string {
	qs := strconv.Quote(s)
	return qs[1 : len(qs)-1]
}
