// dnss is a tool for encapsulating DNS over HTTPS.
//
// It can act as a DNS-to-HTTPS proxy, exposing a traditional DNS server and
// resolving queries using any DNS-over-HTTP (DoH) server.
//
// It can also act as an HTTPS-to-DNS proxy, so you can use it as a DoH server
// if you want more control over the servers and the final DNS server used
// (for example if you are in an isolated environment, such as a test lab or a
// private network).
//
// See the README.md file for more details.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"blitiri.com.ar/go/dnss/internal/dnsserver"
	"blitiri.com.ar/go/dnss/internal/httpresolver"
	"blitiri.com.ar/go/dnss/internal/httpserver"
	"blitiri.com.ar/go/log"

	// Register pprof handlers for monitoring and debugging.
	_ "net/http/pprof"
)

var (
	dnsListenAddr = flag.String("dns_listen_addr", ":53",
		"address to listen on for DNS")

	dnsUnqualifiedUpstream = flag.String("dns_unqualified_upstream", "",
		"DNS server to forward unqualified requests to")
	dnsServerForDomain = flag.String("dns_server_for_domain", "",
		"DNS server to use for a specific domain, "+
			`in the form of "domain1:addr1, domain2:addr, ..."`)

	fallbackUpstream = flag.String("fallback_upstream", "8.8.8.8:53",
		"DNS server used to resolve domains in -https_upstream"+
			" (including proxy if needed)")

	enableDNStoHTTPS = flag.Bool("enable_dns_to_https", false,
		"enable DNS-to-HTTPS proxy")
	httpsUpstream = flag.String("https_upstream",
		"https://dns.google/dns-query",
		"URL of upstream DNS-to-HTTP server")
	httpsClientCAFile = flag.String("https_client_cafile", "",
		"CA file to use for the HTTPS client")
	enableCache = flag.Bool("enable_cache", true, "enable the local cache")

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
	insecureHTTPServer = flag.Bool("insecure_http_server", false,
		"listen on plain HTTP, not HTTPS")

	monitoringListenAddr = flag.String("monitoring_listen_addr", "",
		"address to listen on for monitoring HTTP requests")

	// Deprecated flags that no longer make sense; we keep them for backwards
	// compatibility but may be removed in the future.
	_ = flag.Duration("log_flush_every", 0, "deprecated, will be removed")
	_ = flag.Bool("logtostderr", false, "deprecated, will be removed")
	_ = flag.String("force_mode", "", "deprecated, will be removed")
	_ = flag.String("fallback_domains", "", "deprecated, will be removed")
)

func main() {
	flag.Parse()
	log.Init()

	go signalHandler()

	if *monitoringListenAddr != "" {
		launchMonitoringServer(*monitoringListenAddr)
	}

	if !(*enableDNStoHTTPS || *enableHTTPStoDNS) {
		log.Errorf("Need to set one of the following:")
		log.Errorf("  --enable_dns_to_https")
		log.Errorf("  --enable_https_to_dns")
		log.Fatalf("")
	}

	var wg sync.WaitGroup

	// DNS to HTTPS.
	if *enableDNStoHTTPS {
		upstream, err := url.Parse(*httpsUpstream)
		if err != nil {
			log.Fatalf("-https_upstream is not a valid URL: %v", err)
		}

		var resolver dnsserver.Resolver
		resolver = httpresolver.NewDoH(upstream, *httpsClientCAFile, *fallbackUpstream)

		if *enableCache {
			cr := dnsserver.NewCachingResolver(resolver)
			cr.RegisterDebugHandlers()
			resolver = cr
		}

		overrides, err := dnsserver.DomainMapFromString(*dnsServerForDomain)
		if err != nil {
			log.Fatalf("-dns_server_for_domain is not valid: %v", err)
		}

		dth := dnsserver.New(*dnsListenAddr, resolver,
			*dnsUnqualifiedUpstream, overrides)

		wg.Add(1)
		go func() {
			defer wg.Done()
			dth.ListenAndServe()
		}()
	}

	// HTTPS to DNS.
	if *enableHTTPStoDNS {
		s := httpserver.Server{
			Addr:     *httpsAddr,
			Upstream: *dnsUpstream,
			CertFile: *httpsCertFile,
			KeyFile:  *httpsKeyFile,
			Insecure: *insecureHTTPServer,
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ListenAndServe()
		}()
	}

	wg.Wait()
}

func signalHandler() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)

	for sig := range signals {
		switch sig {
		case syscall.SIGTERM, syscall.SIGINT:
			log.Fatalf("Got signal to exit: %v", sig)
		}
	}
}

func launchMonitoringServer(addr string) {
	log.Infof("Monitoring HTTP server listening on %s", addr)

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
          <li><a href="/debug/requests?fam=dnsserver&b=11">dns server latency</a>
          <li><a href="/debug/requests?fam=dnsserver&b=0&exp=1">dns server trace</a>
        </ul>
      <li><a href="/debug/dnsserver/cache/dump">cache dump</a>
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
