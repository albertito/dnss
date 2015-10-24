package main

import (
	"flag"
	"sync"
	"time"

	// Register pprof handlers for monitoring and debugging.
	"net/http"
	_ "net/http/pprof"

	"github.com/golang/glog"

	// Make GRPC log to glog.
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/grpclog/glogger"

	"blitiri.com.ar/go/dnss/dnstogrpc"
	"blitiri.com.ar/go/dnss/grpctodns"
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
	dnsUnqualifiedUpstream = flag.String("dns_unqualified_upstream", "",
		"DNS server to forward unqualified requests to")

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

	logFlushEvery = flag.Duration("log_flush_every", 30*time.Second,
		"how often to flush logs")
	monitoringListenAddr = flag.String("monitoring_listen_addr", "",
		"address to listen on for monitoring HTTP requests")
)

func flushLogs() {
	c := time.Tick(*logFlushEvery)
	for range c {
		glog.Flush()
	}
}

func main() {
	defer glog.Flush()

	flag.Parse()

	go flushLogs()

	grpc.EnableTracing = false
	if *monitoringListenAddr != "" {
		glog.Infof("Monitoring HTTP server listening on %s",
			*monitoringListenAddr)
		grpc.EnableTracing = true
		go http.ListenAndServe(*monitoringListenAddr, nil)
	}

	if !*enableDNStoGRPC && !*enableGRPCtoDNS {
		glog.Fatal(
			"Error: pass --enable_dns_to_grpc or --enable_grpc_to_dns")
	}

	var wg sync.WaitGroup

	// DNS to GRPC.
	if *enableDNStoGRPC {
		r := dnstogrpc.NewGRPCResolver(*grpcUpstream, *grpcClientCAFile)
		dtg := dnstogrpc.New(*dnsListenAddr, r, *dnsUnqualifiedUpstream)
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
