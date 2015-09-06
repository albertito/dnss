// Generate the protobuf+grpc service.
//go:generate protoc --go_out=plugins=grpc:. dnss.proto

package main

import (
	"flag"

	"blitiri.com.ar/go/dnss/dnstogrpc"
	"blitiri.com.ar/go/profile"
)

var (
	dnsaddr     = flag.String("dnsaddr", ":53", "address to listen on")
	dnsupstream = flag.String("dnsupstream", "8.8.8.8:53", "upstream address")
)

func main() {
	flag.Parse()

	profile.Init()

	dtg := &dnstogrpc.Server{
		Addr:     *dnsaddr,
		Upstream: *dnsupstream,
	}
	dtg.ListenAndServe()
}
