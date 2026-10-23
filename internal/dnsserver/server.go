// Package dnsserver implements a DNS server, that uses the given resolvers to
// handle requests.
package dnsserver

import (
	"context"
	"net"
	"sync"

	"blitiri.com.ar/go/dnss/internal/trace"

	"blitiri.com.ar/go/log"
	"blitiri.com.ar/go/systemd"
	"github.com/miekg/dns"
)

// Server implements a DNS proxy, which will (mostly) use the given resolver
// to resolve queries.
type Server struct {
	Addr            string
	unqUpstream     string
	serverOverrides DomainMap
	resolver        Resolver
}

// New *Server, which will listen on addr, use resolver as the backend
// resolver, and use unqUpstream to resolve unqualified queries.
func New(addr string, resolver Resolver, unqUpstream string, serverOverrides DomainMap) *Server {
	return &Server{
		Addr:            addr,
		resolver:        resolver,
		unqUpstream:     unqUpstream,
		serverOverrides: serverOverrides,
	}
}

// Handler for the incoming DNS queries.
func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	tr := trace.New("dnsserver.Handler",
		w.RemoteAddr().Network()+" "+w.RemoteAddr().String())
	defer tr.Finish()

	tr.Printf("id:%v", r.Id)
	tr.Question(r.Question)

	// We only support single-question queries.
	if len(r.Question) != 1 {
		tr.Printf("len(Q) != 1, failing")
		dns.HandleFailed(w, r)
		return
	}

	// If the domain has a server override, forward to it instead.
	override, ok := s.serverOverrides.GetMostSpecific(r.Question[0].Name)
	if ok {
		tr.Printf("override found: %q", override)
		u, err := dns.Exchange(r, override)
		if err == nil {
			tr.Answer(u)
			s.writeReply(tr, w, r, u)
		} else {
			tr.Printf("override server returned error: %v", err)
			dns.HandleFailed(w, r)
		}

		return
	}

	// Forward to the unqualified upstream server if:
	//  - We have one configured.
	//  - There's only one question in the request, to keep things simple.
	//  - The question is unqualified (only one '.' in the name).
	useUnqUpstream := s.unqUpstream != "" &&
		dns.CountLabel(r.Question[0].Name) <= 1
	if useUnqUpstream {
		u, err := dns.Exchange(r, s.unqUpstream)
		if err == nil {
			tr.Printf("used unqualified upstream")
			tr.Answer(u)
			s.writeReply(tr, w, r, u)
		} else {
			tr.Printf("unqualified upstream error: %v", err)
			dns.HandleFailed(w, r)
		}

		return
	}

	// Create our own (random) IDs, in case different users pick the same id
	// and we pass that upstream.
	oldid := r.Id
	r.Id = dns.Id()

	fromUp, err := s.resolver.Query(r, tr)
	if err != nil {
		log.Infof("resolver query error: %v", err)
		tr.Error(err)

		r.Id = oldid
		dns.HandleFailed(w, r)
		return
	}

	tr.Answer(fromUp)

	fromUp.Id = oldid
	s.writeReply(tr, w, r, fromUp)
}

func (s *Server) writeReply(tr *trace.Trace, w dns.ResponseWriter, r, reply *dns.Msg) {
	// We need to check if the response fits.
	// UDP by default has a maximum of 512 bytes. This can be extended via
	// the client in the EDNS0 record. TCP has a maximum of 64 KiB.
	network := w.RemoteAddr().Network()
	maxSize := dns.MaxMsgSize
	if network == "udp" {
		maxSize = 512
		ednsOPT := r.IsEdns0()
		if ednsOPT != nil {
			maxSize = int(ednsOPT.UDPSize())
		}
	}
	reply.Truncate(maxSize)
	tr.Printf("%s max:%d truncated:%v", network, maxSize, reply.Truncated)

	// Always compress the reply. Messages are unpacked without compression,
	// and packing them back as-is can make them much larger, even beyond what
	// TCP can carry.
	// Note this must be done after Truncate, which resets it.
	reply.Compress = true

	if err := w.WriteMsg(reply); err != nil {
		tr.Errorf("error writing reply: %v", err)
	}
}

// ListenAndServe launches the DNS proxy.
func (s *Server) ListenAndServe() {
	err := s.resolver.Init()
	if err != nil {
		log.Fatalf("Error initializing: %v", err)
	}

	go s.resolver.Maintain(context.Background())

	if s.Addr == "systemd" {
		s.systemdServe()
	} else {
		s.classicServe()
	}
}

func (s *Server) classicServe() {
	log.Infof("DNS listening on %s", s.Addr)

	var wg sync.WaitGroup
	wg.Go(func() {
		err := dns.ListenAndServe(s.Addr, "udp", dns.HandlerFunc(s.Handler))
		log.Fatalf("Exiting UDP: %v", err)
	})

	wg.Go(func() {
		err := dns.ListenAndServe(s.Addr, "tcp", dns.HandlerFunc(s.Handler))
		log.Fatalf("Exiting TCP: %v", err)
	})

	wg.Wait()
}

func (s *Server) systemdServe() {
	fsMap, err := systemd.Files()
	if err != nil {
		log.Fatalf("Error getting systemd listeners: %v", err)
	}

	// We will usually have at least one TCP socket and one UDP socket.
	// PacketConns are UDP sockets, Listeners are TCP sockets.
	pconns := []net.PacketConn{}
	listeners := []net.Listener{}
	for _, fs := range fsMap {
		for _, f := range fs {
			if lis, err := net.FileListener(f); err == nil {
				listeners = append(listeners, lis)
				f.Close()
			} else if pc, err := net.FilePacketConn(f); err == nil {
				pconns = append(pconns, pc)
				f.Close()
			}
		}
	}

	var wg sync.WaitGroup

	for _, pconn := range pconns {
		wg.Go(func() {
			log.Infof("Activate on packet connection (UDP): %v", pconn.LocalAddr())
			err := dns.ActivateAndServe(nil, pconn, dns.HandlerFunc(s.Handler))
			log.Fatalf("Exiting UDP listener: %v", err)
		})
	}

	for _, lis := range listeners {
		wg.Go(func() {
			log.Infof("Activate on listening socket (TCP): %v", lis.Addr())
			err := dns.ActivateAndServe(lis, nil, dns.HandlerFunc(s.Handler))
			log.Fatalf("Exiting TCP listener: %v", err)
		})
	}

	wg.Wait()

	// We should only get here if there were no useful sockets.
	log.Fatalf("No systemd sockets, did you forget the .socket?")
}
