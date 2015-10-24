// DNS to GRPC.

package dnstogrpc

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/miekg/dns"
	"golang.org/x/net/trace"

	"blitiri.com.ar/go/dnss/internal/util"
)

// newID is a channel used to generate new request IDs.
// There is a goroutine created at init() time that will get IDs randomly, to
// help prevent guesses.
var newId chan uint16

func init() {
	// Buffer 100 numbers to avoid blocking on crypto rand.
	newId = make(chan uint16, 100)

	go func() {
		var id uint16
		var err error

		for {
			err = binary.Read(rand.Reader, binary.LittleEndian, &id)
			if err != nil {
				panic(fmt.Sprintf("error creating id: %v", err))
			}

			newId <- id
		}

	}()
}

type Server struct {
	Addr        string
	unqUpstream string
	resolver    Resolver
}

func New(addr string, resolver Resolver, unqUpstream string) *Server {
	return &Server{
		Addr:        addr,
		resolver:    resolver,
		unqUpstream: unqUpstream,
	}
}

func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	tr := trace.New("dnstogrpc", "Handler")
	defer tr.Finish()

	tr.LazyPrintf("from:%v   id:%v", w.RemoteAddr(), r.Id)

	if glog.V(3) {
		tr.LazyPrintf(util.QuestionsToString(r.Question))
	}

	// Forward to the unqualified upstream server if:
	//  - We have one configured.
	//  - There's only one question in the request, to keep things simple.
	//  - The question is unqualified (only one '.' in the name).
	useUnqUpstream := s.unqUpstream != "" && len(r.Question) == 1 &&
		strings.Count(r.Question[0].Name, ".") <= 1
	if useUnqUpstream {
		u, err := dns.Exchange(r, s.unqUpstream)
		if err == nil {
			tr.LazyPrintf("used unqualified upstream")
			if glog.V(3) {
				util.TraceAnswer(tr, u)
			}
			w.WriteMsg(u)
			return
		} else {
			tr.LazyPrintf("unqualified upstream error: %v", err)
		}
	}

	// Create our own IDs, in case different users pick the same id and we
	// pass that upstream.
	oldid := r.Id
	r.Id = <-newId

	from_up, err := s.resolver.Query(r, tr)
	if err != nil {
		glog.Infof(err.Error())
		tr.LazyPrintf(err.Error())
		tr.SetError()
		return
	}

	if glog.V(3) {
		util.TraceAnswer(tr, from_up)
	}

	from_up.Id = oldid
	w.WriteMsg(from_up)
}

func (s *Server) ListenAndServe() {
	err := s.resolver.Init()
	if err != nil {
		glog.Errorf("Error initializing: %v", err)
		return
	}

	go s.resolver.Maintain()

	glog.Infof("DNS listening on %s", s.Addr)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "udp", dns.HandlerFunc(s.Handler))
		glog.Errorf("Exiting UDP: %v", err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "tcp", dns.HandlerFunc(s.Handler))
		glog.Errorf("Exiting TCP: %v", err)
	}()

	wg.Wait()
}
