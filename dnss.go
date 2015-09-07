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
	dnsaddr = flag.String("dnsaddr", ":53",
		"address to listen on for DNS")
	grpcupstream = flag.String("grpcupstream", "localhost:9953",
		"address of the upstream GRPC server")
	grpc_client_cafile = flag.String("grpc_client_cafile", "",
		"CA file to use for the GRPC client")

	enableGRPCtoDNS = flag.Bool("enable_grpc_to_dns", false,
		"enable GRPC-to-DNS server")
	grpcaddr = flag.String("grpcaddr", ":9953",
		"address to listen on for GRPC")
	dnsupstream = flag.String("dnsupstream", "8.8.8.8:53",
		"address of the upstream DNS server")

	grpccert = flag.String("grpccert", "",
		"certificate file for the GRPC server")
	grpckey = flag.String("grpckey", "",
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
		dtg := dnstogrpc.New(*dnsaddr, *grpcupstream, *grpc_client_cafile)
		wg.Add(1)
		go func() {
			defer wg.Done()
			dtg.ListenAndServe()
		}()
	}

	// GRPC to DNS.
	if *enableGRPCtoDNS {
		gtd := &grpctodns.Server{
			Addr:     *grpcaddr,
			Upstream: *dnsupstream,
			CertFile: *grpccert,
			KeyFile:  *grpckey,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			gtd.ListenAndServe()
		}()
	}

	wg.Wait()
}
