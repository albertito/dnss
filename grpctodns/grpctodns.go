// GRPC to DNS.

package grpctodns

import (
	"fmt"
	"log"
	"net"
	"strings"

	pb "blitiri.com.ar/go/dnss/proto"
	"blitiri.com.ar/go/dnss/util"
	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
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

type Server struct {
	Addr     string
	Upstream string
}

func (s *Server) Query(ctx context.Context, in *pb.RawMsg) (*pb.RawMsg, error) {
	r := &dns.Msg{}
	err := r.Unpack(in.Data)
	if err != nil {
		return nil, err
	}

	log.Printf("GRPC %v\n", util.QuestionsToString(r.Question))

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.
	from_up, err := dns.Exchange(r, s.Upstream)
	if err != nil {
		log.Printf("GRPC   ERR: %v\n", err)
		return nil, err
	}

	if from_up == nil {
		return nil, fmt.Errorf("No response from upstream")
	}

	if from_up.Rcode != dns.RcodeSuccess {
		rcode := dns.RcodeToString[from_up.Rcode]
		log.Printf("GPRC   !->  %v\n", rcode)
	}
	for _, rr := range from_up.Answer {
		log.Printf("GRPC   ->  %v\n", rr)
	}

	buf, err := from_up.Pack()
	if err != nil {
		log.Printf("GRPC   ERR: %v\n", err)
		return nil, err
	}

	return &pb.RawMsg{Data: buf}, nil
}

func (s *Server) ListenAndServe() {
	lis, err := net.Listen("tcp", s.Addr)
	if err != nil {
		log.Printf("failed to listen: %v", err)
		return
	}

	// TODO: TLS

	grpcServer := grpc.NewServer()
	pb.RegisterDNSServiceServer(grpcServer, s)

	log.Printf("GRPC listening on %s\n", s.Addr)
	grpcServer.Serve(lis)
}
