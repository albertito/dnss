// DNS to GRPC.

package dnstogrpc

import (
	"fmt"
	"sync"
	"time"

	pb "blitiri.com.ar/go/dnss/proto"
	"blitiri.com.ar/go/dnss/util"
	"blitiri.com.ar/go/l"
	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

type grpcclient struct {
	Upstream string
	client   pb.DNSServiceClient
}

func (c *grpcclient) Connect() error {
	// TODO: TLS
	conn, err := grpc.Dial(c.Upstream, grpc.WithInsecure())
	if err != nil {
		return err
	}

	c.client = pb.NewDNSServiceClient(conn)
	return nil
}

func (c *grpcclient) Query(r *dns.Msg) (*dns.Msg, error) {
	buf, err := r.Pack()
	if err != nil {
		return nil, err
	}

	// Give our RPCs 2 second timeouts: DNS usually doesn't wait that long
	// anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	g, err := c.client.Query(ctx, &pb.RawMsg{Data: buf})
	if err != nil {
		return nil, err
	}

	m := &dns.Msg{}
	err = m.Unpack(g.Data)
	return m, err
}

type Server struct {
	Addr string

	client *grpcclient
}

func New(addr, upstream string) *Server {
	return &Server{
		Addr:   addr,
		client: &grpcclient{Upstream: upstream},
	}
}

func p(w dns.ResponseWriter, r *dns.Msg) string {
	return fmt.Sprintf("%v %v", w.RemoteAddr(), r.Id)
}

func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	l.Printf("DNS  %v %v\n", p(w, r), util.QuestionsToString(r.Question))

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.

	from_up, err := s.client.Query(r)
	if err != nil {
		l.Printf("DNS  %v  ERR: %v\n", p(w, r), err)
	}

	if from_up != nil {
		if from_up.Rcode != dns.RcodeSuccess {
			rcode := dns.RcodeToString[from_up.Rcode]
			l.Printf("DNS  %v  !->  %v\n", p(w, r), rcode)
		}
		for _, rr := range from_up.Answer {
			l.Printf("DNS  %v  ->  %v\n", p(w, r), rr)
		}
		w.WriteMsg(from_up)
	}
}

func (s *Server) ListenAndServe() {
	err := s.client.Connect()
	if err != nil {
		// TODO: handle errors and reconnect.
		l.Printf("Error creating GRPC client: %v\n", err)
		return
	}

	l.Printf("DNS listening on %s\n", s.Addr)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "udp", dns.HandlerFunc(s.Handler))
		l.Printf("Exiting UDP: %v\n", err)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := dns.ListenAndServe(s.Addr, "tcp", dns.HandlerFunc(s.Handler))
		l.Printf("Exiting TCP: %v\n", err)
	}()

	wg.Wait()
}
