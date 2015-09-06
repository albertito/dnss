package main

import (
	"flag"
	"sync"

	"blitiri.com.ar/go/dnss/dnstogrpc"
	"blitiri.com.ar/go/dnss/grpctodns"
	"blitiri.com.ar/go/profile"
)

var (
	dnsaddr = flag.String("dnsaddr", ":53",
		"address to listen on for DNS")
	grpcupstream = flag.String("grpcupstream", "localhost:9953",
		"address of the upstream GRPC server")

	grpcaddr = flag.String("grpcaddr", ":9953",
		"address to listen on for GRPC")
	dnsupstream = flag.String("dnsupstream", "8.8.8.8:53",
		"address of the upstream DNS server")
)

func main() {
	flag.Parse()

	profile.Init()

	var wg sync.WaitGroup

	// DNS to GRPC.
	dtg := &dnstogrpc.Server{
		Addr:     *dnsaddr,
		Upstream: *grpcupstream,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		dtg.ListenAndServe()
	}()

	// GRPC to DNS.
	gtd := &grpctodns.Server{
		Addr:     *grpcaddr,
		Upstream: *dnsupstream,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		gtd.ListenAndServe()
	}()

	wg.Wait()
}
