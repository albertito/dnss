package main

import (
	"flag"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"blitiri.com.ar/go/dnss/internal/dnstohttps"
	"blitiri.com.ar/go/dnss/internal/httpstodns"

	"github.com/golang/glog"

	// Register pprof handlers for monitoring and debugging.
	_ "net/http/pprof"
)

var (
	dnsListenAddr = flag.String("dns_listen_addr", ":53",
		"address to listen on for DNS")

	dnsUnqualifiedUpstream = flag.String("dns_unqualified_upstream", "",
		"DNS server to forward unqualified requests to")

	fallbackUpstream = flag.String("fallback_upstream", "8.8.8.8:53",
		"DNS server to resolve domains in --fallback_domains")
	fallbackDomains = flag.String("fallback_domains", "dns.google.com.",
		"Domains we resolve via DNS, using --fallback_upstream"+
			" (space-separated list)")

	enableDNStoHTTPS = flag.Bool("enable_dns_to_https", false,
		"enable DNS-to-HTTPS proxy")
	httpsUpstream = flag.String("https_upstream",
		"https://dns.google.com/resolve",
		"URL of upstream DNS-to-HTTP server")
	httpsClientCAFile = flag.String("https_client_cafile", "",
		"CA file to use for the HTTPS client")

	enableHTTPStoDNS = flag.Bool("enable_https_to_dns", false,
		"enable HTTPS-to-DNS proxy")
	dnsUpstream = flag.String("dns_upstream",
		"8.8.8.8:53",
		"Address of the upstream DNS server (for the HTTPS-to-DNS proxy)")
	httpsCertFile = flag.String("https_cert", "",
		"certificate to use for the HTTPS server")
	httpsKeyFile = flag.String("https_key", "",
		"key to use for the HTTPS server")
	httpsAddr = flag.String("https_server_addr", ":443",
		"address to listen on for HTTPS-to-DNS requests")

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

	if *monitoringListenAddr != "" {
		launchMonitoringServer(*monitoringListenAddr)
	}

	if !(*enableDNStoHTTPS || *enableHTTPStoDNS) {
		glog.Error("Need to set one of the following:")
		glog.Error("  --enable_dns_to_https")
		glog.Error("  --enable_https_to_dns")
		glog.Fatal("")
	}

	var wg sync.WaitGroup

	// DNS to HTTPS.
	if *enableDNStoHTTPS {
		r := dnstohttps.NewHTTPSResolver(*httpsUpstream, *httpsClientCAFile)
		cr := dnstohttps.NewCachingResolver(r)
		cr.RegisterDebugHandlers()
		dth := dnstohttps.New(*dnsListenAddr, cr, *dnsUnqualifiedUpstream)
		dth.SetFallback(
			*fallbackUpstream, strings.Split(*fallbackDomains, " "))
		wg.Add(1)
		go func() {
			defer wg.Done()
			dth.ListenAndServe()
		}()
	}

	// HTTPS to DNS.
	if *enableHTTPStoDNS {
		s := httpstodns.Server{
			Addr:     *httpsAddr,
			Upstream: *dnsUpstream,
			CertFile: *httpsCertFile,
			KeyFile:  *httpsKeyFile,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ListenAndServe()
		}()
	}

	wg.Wait()
}

func launchMonitoringServer(addr string) {
	glog.Infof("Monitoring HTTP server listening on %s", addr)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(monitoringHTMLIndex))
	})

	flags := dumpFlags()
	http.HandleFunc("/debug/flags", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(flags))
	})

	go http.ListenAndServe(addr, nil)
}

// Static index for the monitoring website.
const monitoringHTMLIndex = `<!DOCTYPE html>
<html>
  <head>
    <title>dnss monitoring</title>
  </head>
  <body>
    <h1>dnss monitoring</h1>
    <ul>
      <li><a href="/debug/requests">requests</a>
          <small><a href="https://godoc.org/golang.org/x/net/trace">
            (ref)</a></small>
        <ul>
          <li><a href="/debug/requests?fam=dnstohttp&b=11">dnstohttp latency</a>
          <li><a href="/debug/requests?fam=dnstohttp&b=0&exp=1">dnstohttp trace</a>
        </ul>
      <li><a href="/debug/dnstohttp/cache/dump">cache dump</a>
      <li><a href="/debug/pprof">pprof</a>
          <small><a href="https://golang.org/pkg/net/http/pprof/">
            (ref)</a></small>
        <ul>
          <li><a href="/debug/pprof/goroutine?debug=1">goroutines</a>
        </ul>
      <li><a href="/debug/flags">flags</a>
      <li><a href="/debug/vars">public variables</a>
    </ul>
  </body>
</html>
`

// dumpFlags to a string, for troubleshooting purposes.
func dumpFlags() string {
	s := ""
	visited := make(map[string]bool)

	// Print set flags first, then the rest.
	flag.Visit(func(f *flag.Flag) {
		s += fmt.Sprintf("-%s=%s\n", f.Name, f.Value.String())
		visited[f.Name] = true
	})

	s += "\n"
	flag.VisitAll(func(f *flag.Flag) {
		if !visited[f.Name] {
			s += fmt.Sprintf("-%s=%s\n", f.Name, f.Value.String())
		}
	})

	return s
}
