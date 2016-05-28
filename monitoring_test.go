package main

// Tests for the monitoring server.
//
// Note that functional tests for dnss are in the testing/ directory, here we
// only test the monitoring server created in dnss.go.

import (
	"net/http"
	"testing"

	"google.golang.org/grpc"
)

func TestMonitoringServer(t *testing.T) {
	// TODO: Don't hard-code this.
	const addr = "localhost:19395"
	launchMonitoringServer(addr)

	if !grpc.EnableTracing {
		t.Errorf("grpc tracing is disabled")
	}

	checkGet(t, "http://"+addr+"/")
	checkGet(t, "http://"+addr+"/debug/requests")
	checkGet(t, "http://"+addr+"/debug/pprof/goroutine")
	checkGet(t, "http://"+addr+"/debug/flags")
	checkGet(t, "http://"+addr+"/debug/vars")

	// Check that we emit 404 for non-existing paths.
	r, _ := http.Get("http://" + addr + "/doesnotexist")
	if r.StatusCode != 404 {
		t.Errorf("expected 404, got %s", r.Status)
	}
}

func checkGet(t *testing.T, url string) {
	r, err := http.Get(url)
	if err != nil {
		t.Error(err)
		return
	}

	if r.StatusCode != 200 {
		t.Errorf("%q - invalid status: %s", url, r.Status)
	}

}
