package main

import (
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"blitiri.com.ar/go/dnss/internal/nettrace"
	"blitiri.com.ar/go/log"
)

var (
	Version    = ""
	SourceDate = time.Time{}
)

func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		panic("unable to read build info")
	}

	dirty := false
	gitRev := ""
	gitTime := ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.modified":
			if s.Value == "true" {
				dirty = true
			}
		case "vcs.time":
			gitTime = s.Value
		case "vcs.revision":
			gitRev = s.Value
		}
	}

	SourceDate, _ = time.Parse(time.RFC3339, gitTime)

	Version = SourceDate.Format("20060102")
	if gitRev != "" {
		Version += fmt.Sprintf("-%.9s", gitRev)
	}
	if dirty {
		Version += "-dirty"
	}
}

func monitoringServer(addr string) {
	log.Infof("Monitoring HTTP server listening on %s", addr)

	http.HandleFunc("/", debugRoot())
	nettrace.RegisterHandler(http.DefaultServeMux)

	go http.ListenAndServe(addr, nil)
}

func debugRoot() http.HandlerFunc {
	hostname, _ := os.Hostname()
	fSet, fUnset := flagsLists()

	indexData := struct {
		Hostname   string
		Version    string
		GoVersion  string
		SourceDate time.Time
		StartTime  time.Time
		Args       []string
		Env        []string
		FlagsSet   []string
		FlagsUnset []string
	}{
		Hostname:   hostname,
		Version:    Version,
		GoVersion:  runtime.Version(),
		SourceDate: SourceDate,
		StartTime:  time.Now(),
		Args:       os.Args,
		Env:        os.Environ(),
		FlagsSet:   fSet,
		FlagsUnset: fUnset,
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if err := htmlIndex.Execute(w, indexData); err != nil {
			log.Infof("Monitoring handler error: %v", err)
		}
	}
}

var tmplFuncs = template.FuncMap{
	"since": time.Since,
	"roundDuration": func(d time.Duration) time.Duration {
		return d.Round(time.Second)
	},
}

// Static index for the monitoring website.
var htmlIndex = template.Must(
	template.New("index").Funcs(tmplFuncs).Parse(
		`<!DOCTYPE html>
<html>

<head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>dnss @{{.Hostname}}</title>
<style type="text/css">
  body {
    font-family: sans-serif;
  }
  @media (prefers-color-scheme: dark) {
    body {
      background: #121212;
      color: #c9d1d9;
    }
    a { color: #44b4ec; }
  }
</style>
</head>

<body>
  <h1>dnss @{{.Hostname}}</h1>

  version {{.Version}}<br>
  source date {{.SourceDate.Format "2006-01-02 15:04:05 -0700"}}<br>
  built with: {{.GoVersion}}<p>

  started {{.StartTime.Format "Mon, 2006-01-02 15:04:05 -0700"}}<br>
  up for {{.StartTime | since | roundDuration}}<br>
  os hostname <i>{{.Hostname}}</i><br>
  <p>

  <ul>
    <li><a href="/debug/traces">traces</a>
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

  <details>
  <summary>flags</summary>
  <ul>
    {{range .FlagsSet}}<li><tt>{{.}}</tt>{{end}}
    {{range .FlagsUnset}}<li><i><tt>{{.}}</tt></i>{{end}}
  </ul>
  </details>
  <p>

  <details>
  <summary>env</summary>
  <ul> {{range .Env}}<li><tt>{{.}}</tt>{{end}} </ul>
  </details>

</body>
</html>
`))

func flagsLists() (set, unset []string) {
	visited := make(map[string]bool)
	// Print set flags first, then the rest.
	flag.Visit(func(f *flag.Flag) {
		set = append(set, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
		visited[f.Name] = true
	})

	flag.VisitAll(func(f *flag.Flag) {
		if visited[f.Name] {
			return
		}

		unset = append(unset, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
	})

	return set, unset
}
