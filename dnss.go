package main

import (
	"flag"
	"log"
	"sync"

	"blitiri.com.ar/go/dnss/dnstogrpc"
	"blitiri.com.ar/go/dnss/grpctodns"
	"blitiri.com.ar/go/logconfig"
	"blitiri.com.ar/go/profile"
)

var (
	enableDNStoGRPC = flag.Bool("enable_dns_to_grpc", false,
		"enable DNS-to-GRPC server")
	dnsListenAddr = flag.String("dns_listen_addr", ":53",
		"address to listen on for DNS")
	grpcUpstream = flag.String("grpc_upstream", "localhost:9953",
		"address of the upstream GRPC server")
	grpcClientCAFile = flag.String("grpc_client_cafile", "",
		"CA file to use for the GRPC client")

	enableGRPCtoDNS = flag.Bool("enable_grpc_to_dns", false,
		"enable GRPC-to-DNS server")
	grpcListenAddr = flag.String("grpc_listen_addr", ":9953",
		"address to listen on for GRPC")
	dnsUpstream = flag.String("dns_upstream", "8.8.8.8:53",
		"address of the upstream DNS server")

	grpcCert = flag.String("grpc_cert", "",
		"certificate file for the GRPC server")
	grpcKey = flag.String("grpc_key", "",
		"key file for the GRPC server")
)

func main() {
	flag.Parse()

	logconfig.Init("dnss")
	profile.Init()

	if !*enableDNStoGRPC && !*enableGRPCtoDNS {
		log.Fatalf(
			"ERROR: pass --enable_dns_to_grpc or --enable_grpc_to_dns\n")
	}

	var wg sync.WaitGroup

	// DNS to GRPC.
	if *enableDNStoGRPC {
		dtg := dnstogrpc.New(*dnsListenAddr, *grpcUpstream, *grpcClientCAFile)
		wg.Add(1)
		go func() {
			defer wg.Done()
			dtg.ListenAndServe()
		}()
	}

	// GRPC to DNS.
	if *enableGRPCtoDNS {
		gtd := &grpctodns.Server{
			Addr:     *grpcListenAddr,
			Upstream: *dnsUpstream,
			CertFile: *grpcCert,
			KeyFile:  *grpcKey,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			gtd.ListenAndServe()
		}()
	}

	wg.Wait()
}
