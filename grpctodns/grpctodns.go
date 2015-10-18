// GRPC to DNS.

package grpctodns

import (
	"fmt"
	"net"
	"strings"

	pb "blitiri.com.ar/go/dnss/internal/proto"
	"blitiri.com.ar/go/dnss/internal/util"
	"github.com/golang/glog"
	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"golang.org/x/net/trace"
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
	tr := trace.New("grpctodns", "Query")
	defer tr.Finish()

	r := &dns.Msg{}
	err := r.Unpack(in.Data)
	if err != nil {
		return nil, err
	}

	if glog.V(3) {
		tr.LazyPrintf(util.QuestionsToString(r.Question))
	}

	// TODO: we should create our own IDs, in case different users pick the
	// same id and we pass that upstream.
	from_up, err := dns.Exchange(r, s.Upstream)
	if err != nil {
		msg := fmt.Sprintf("dns exchange error: %v", err)
		glog.Info(msg)
		tr.LazyPrintf(msg)
		tr.SetError()
		return nil, err
	}

	if from_up == nil {
		err = fmt.Errorf("no response from upstream")
		tr.LazyPrintf(err.Error())
		tr.SetError()
		return nil, err
	}

	if glog.V(3) {
		util.TraceAnswer(tr, from_up)
	}

	buf, err := from_up.Pack()
	if err != nil {
		glog.Infof("   error packing: %v", err)
		tr.LazyPrintf("error packing: %v", err)
		tr.SetError()
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

	glog.Infof("GRPC listening on %s", s.Addr)
	err = grpcServer.Serve(lis)
	glog.Infof("GRPC exiting: %s", err)
}
