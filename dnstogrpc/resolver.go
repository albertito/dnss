package dnstogrpc

import (
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "blitiri.com.ar/go/dnss/internal/proto"
)

// Interface for DNS resolvers that can answer queries.
type Resolver interface {
	// Initialize the resolver.
	Init() error

	// Maintain performs resolver maintenance. It's expected to run
	// indefinitely, but may return early if appropriate.
	Maintain()

	// Query responds to a DNS query.
	Query(r *dns.Msg) (*dns.Msg, error)
}

// grpcResolver implements the Resolver interface by querying a server via
// GRPC.
type grpcResolver struct {
	Upstream string
	CAFile   string
	client   pb.DNSServiceClient
}

func NewGRPCResolver(upstream, caFile string) *grpcResolver {
	return &grpcResolver{
		Upstream: upstream,
		CAFile:   caFile,
	}
}

func (g *grpcResolver) Init() error {
	var err error
	var creds credentials.TransportAuthenticator
	if g.CAFile == "" {
		creds = credentials.NewClientTLSFromCert(nil, "")
	} else {
		creds, err = credentials.NewClientTLSFromFile(g.CAFile, "")
		if err != nil {
			return err
		}
	}

	conn, err := grpc.Dial(g.Upstream, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}

	g.client = pb.NewDNSServiceClient(conn)
	return nil
}

func (g *grpcResolver) Maintain() {
}

func (g *grpcResolver) Query(r *dns.Msg) (*dns.Msg, error) {
	buf, err := r.Pack()
	if err != nil {
		return nil, err
	}

	// Give our RPCs 2 second timeouts: DNS usually doesn't wait that long
	// anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := g.client.Query(ctx, &pb.RawMsg{Data: buf})
	if err != nil {
		return nil, err
	}

	m := &dns.Msg{}
	err = m.Unpack(reply.Data)
	return m, err
}
