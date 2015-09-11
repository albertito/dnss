// GRPC to DNS.

package grpctodns

import (
	"fmt"
	"net"
	"strings"

	pb "blitiri.com.ar/go/dnss/proto"
	"blitiri.com.ar/go/dnss/util"
	"github.com/golang/glog"
	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	CertFile string
	KeyFile  string
}

func (s *Server) Query(ctx context.Context, in *pb.RawMsg) (*pb.RawMsg, error) {
	r := &dns.Msg{}
	err := r.Unpack(in.Data)
	if err != nil {
		return nil, err
	}

	if glog.V(3) {
		glog.Infof("GRPC %v", util.QuestionsToString(r.Question))
	}

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.
	from_up, err := dns.Exchange(r, s.Upstream)
	if err != nil {
		glog.V(3).Infof("GRPC   ERR: %v", err)
		return nil, err
	}

	if from_up == nil {
		return nil, fmt.Errorf("No response from upstream")
	}

	if from_up.Rcode != dns.RcodeSuccess {
		rcode := dns.RcodeToString[from_up.Rcode]
		glog.V(3).Infof("GPRC   !->  %v", rcode)
	}
	for _, rr := range from_up.Answer {
		glog.V(3).Infof("GRPC   ->  %v", rr)
	}

	buf, err := from_up.Pack()
	if err != nil {
		glog.V(3).Infof("GRPC   ERR: %v", err)
		return nil, err
	}

	return &pb.RawMsg{Data: buf}, nil
}

func (s *Server) ListenAndServe() {
	lis, err := net.Listen("tcp", s.Addr)
	if err != nil {
		glog.Errorf("failed to listen: %v", err)
		return
	}

	ta, err := credentials.NewServerTLSFromFile(s.CertFile, s.KeyFile)
	if err != nil {
		glog.Errorf("failed to create TLS transport auth: %v", err)
		return
	}

	grpcServer := grpc.NewServer(grpc.Creds(ta))
	pb.RegisterDNSServiceServer(grpcServer, s)

	glog.Errorf("GRPC listening on %s", s.Addr)
	err = grpcServer.Serve(lis)
	glog.Errorf("GRPC exiting: %s", err)
}
