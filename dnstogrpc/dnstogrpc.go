// DNS to GRPC.

package dnstogrpc

import (
	"fmt"
	"sync"

	pb "blitiri.com.ar/go/dnss/proto"
	"blitiri.com.ar/go/dnss/util"
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

	g, err := c.client.Query(
		context.Background(),
		&pb.RawMsg{Data: buf})
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

func l(w dns.ResponseWriter, r *dns.Msg) string {
	return fmt.Sprintf("%v %v", w.RemoteAddr(), r.Id)
}

func (s *Server) Handler(w dns.ResponseWriter, r *dns.Msg) {
	fmt.Printf("DNS  %v %v\n", l(w, r), util.QuestionsToString(r.Question))

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.

	from_up, err := s.client.Query(r)
	if err != nil {
		fmt.Printf("DNS  %v  ERR: %v\n", l(w, r), err)
		fmt.Printf("DNS  %v  UP: %v\n", l(w, r), from_up)
	}

	if from_up != nil {
		if from_up.Rcode != dns.RcodeSuccess {
			rcode := dns.RcodeToString[from_up.Rcode]
			fmt.Printf("DNS  %v  !->  %v\n", l(w, r), rcode)
		}
		for _, rr := range from_up.Answer {
			fmt.Printf("DNS  %v  ->  %v\n", l(w, r), rr)
		}
		w.WriteMsg(from_up)
	}
}

func (s *Server) ListenAndServe() {
	err := s.client.Connect()
	if err != nil {
		// TODO: handle errors and reconnect.
		fmt.Printf("Error creating GRPC client: %v\n", err)
		return
	}

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
