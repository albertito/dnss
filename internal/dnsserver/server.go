// Package dnsserver implements a DNS server, that uses the given resolvers to
// handle requests.
package dnsserver

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/coreos/go-systemd/activation"
	"github.com/miekg/dns"
	"golang.org/x/net/trace"

	"blitiri.com.ar/go/dnss/internal/util"
	"blitiri.com.ar/go/log"
)

// newID is a channel used to generate new request IDs.
// There is a goroutine created at init() time that will get IDs randomly, to
// help prevent guesses.
var newID chan uint16

func init() {
	// Buffer 100 numbers to avoid blocking on crypto rand.
	newID = make(chan uint16, 100)

	go func() {
		var id uint16
		var err error

		for {
			err = binary.Read(rand.Reader, binary.LittleEndian, &id)
			if err != nil {
				panic(fmt.Sprintf("error creating id: %v", err))
			}

			newID <- id
		}

	}()
}

// Server implements a DNS proxy, which will (mostly) use the given resolver
// to resolve queries.
type Server struct {
	Addr        string
	unqUpstream string
	resolver    Resolver

	fallbackDomains  map[string]struct{}
	fallbackUpstream string
}

// New *Server, which will listen on addr, use resolver as the backend
// resolver, and use unqUpstream to resolve unqualified queries.
func New(addr string, resolver Resolver, unqUpstream string) *Server {
	return &Server{
		Addr:            addr,
		resolver:        resolver,
		unqUpstream:     unqUpstream,
		fallbackDomains: map[string]struct{}{},
	}
}

// SetFallback upstream server for the given domains.
func (s *Server) SetFallback(upstream string, domains []string) {
	s.fallbackUpstream = upstream
	for _, d := range domains {
		s.fallbackDomains[d] = struct{}{}
	}
}

// Handler for the incoming DNS queries.
func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	tr := trace.New("dnsserver", "Handler")
	defer tr.Finish()

	tr.LazyPrintf("from:%v   id:%v", w.RemoteAddr(), r.Id)

	util.TraceQuestion(tr, r.Question)

	// We only support single-question queries.
	if len(r.Question) != 1 {
		tr.LazyPrintf("len(Q) != 1, failing")
		dns.HandleFailed(w, r)
		return
	}

	// Forward to the unqualified upstream server if:
	//  - We have one configured.
	//  - There's only one question in the request, to keep things simple.
	//  - The question is unqualified (only one '.' in the name).
	useUnqUpstream := s.unqUpstream != "" &&
		strings.Count(r.Question[0].Name, ".") <= 1
	if useUnqUpstream {
		u, err := dns.Exchange(r, s.unqUpstream)
		if err == nil {
			tr.LazyPrintf("used unqualified upstream")
			util.TraceAnswer(tr, u)
			w.WriteMsg(u)
		} else {
			tr.LazyPrintf("unqualified upstream error: %v", err)
			dns.HandleFailed(w, r)
		}

		return
	}

	// Forward to the fallback server if the domain is on our list.
	if _, ok := s.fallbackDomains[r.Question[0].Name]; ok {
		u, err := dns.Exchange(r, s.fallbackUpstream)
		if err == nil {
			tr.LazyPrintf("used fallback upstream (%s)", s.fallbackUpstream)
			util.TraceAnswer(tr, u)
			w.WriteMsg(u)
		} else {
			tr.LazyPrintf("fallback upstream error: %v", err)
			dns.HandleFailed(w, r)
		}

		return
	}

	// Create our own IDs, in case different users pick the same id and we
	// pass that upstream.
	oldid := r.Id
	r.Id = <-newID

	fromUp, err := s.resolver.Query(r, tr)
	if err != nil {
		log.Infof("resolver query error: %v", err)
		tr.LazyPrintf(err.Error())
		tr.SetError()

		r.Id = oldid
		dns.HandleFailed(w, r)
		return
	}

	util.TraceAnswer(tr, fromUp)

	fromUp.Id = oldid
	w.WriteMsg(fromUp)
}

// ListenAndServe launches the DNS proxy.
func (s *Server) ListenAndServe() {
	err := s.resolver.Init()
	if err != nil {
		log.Fatalf("Error initializing: %v", err)
	}

	go s.resolver.Maintain()

	if s.Addr == "systemd" {
		s.systemdServe()
	} else {
		s.classicServe()
	}
}

func (s *Server) classicServe() {
	log.Infof("DNS listening on %s", s.Addr)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "udp", dns.HandlerFunc(s.Handler))
		log.Fatalf("Exiting UDP: %v", err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "tcp", dns.HandlerFunc(s.Handler))
		log.Fatalf("Exiting TCP: %v", err)
	}()

	wg.Wait()
}

func (s *Server) systemdServe() {
	// We will usually have at least one TCP socket and one UDP socket.
	// PacketConns are UDP sockets, Listeners are TCP sockets.
	// To make things more annoying, both can (and usually will) have nil
	// entries for the file descriptors that don't match.
	pconns, err := activation.PacketConns(false)
	if err != nil {
		log.Fatalf("Error getting systemd packet conns: %v", err)
	}

	listeners, err := activation.Listeners(false)
	if err != nil {
		log.Fatalf("Error getting systemd listeners: %v", err)
	}

	var wg sync.WaitGroup

	for _, pconn := range pconns {
		if pconn == nil {
			continue
		}

		wg.Add(1)
		go func(c net.PacketConn) {
			defer wg.Done()
			log.Infof("Activate on packet connection (UDP)")
			err := dns.ActivateAndServe(nil, c, dns.HandlerFunc(s.Handler))
			log.Fatalf("Exiting UDP listener: %v", err)
		}(pconn)
	}

	for _, lis := range listeners {
		if lis == nil {
			continue
		}

		wg.Add(1)
		go func(l net.Listener) {
			defer wg.Done()
			log.Infof("Activate on listening socket (TCP)")
			err := dns.ActivateAndServe(l, nil, dns.HandlerFunc(s.Handler))
			log.Fatalf("Exiting TCP listener: %v", err)
		}(lis)
	}

	wg.Wait()

	// We should only get here if there were no useful sockets.
	log.Fatalf("No systemd sockets, did you forget the .socket?")
}
