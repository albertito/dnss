// Tests for dnss.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io/ioutil"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/miekg/dns"

	"blitiri.com.ar/go/dnss/dnstox"
	"blitiri.com.ar/go/dnss/grpctodns"
)

const (
	// TODO: Don't hard-code these.
	dnsToGrpcAddr = "127.0.0.1:13451"
	grpcToDnsAddr = "127.0.0.1:13452"
	dnsSrvAddr    = "127.0.0.1:13453"
)

//
// === Tests ===
//

func dnsQuery(conn *dns.Conn) error {
	m := &dns.Msg{}
	m.SetQuestion("ca.chai.", dns.TypeMX)

	conn.WriteMsg(m)
	_, err := conn.ReadMsg()
	return err
}

func TestSimple(t *testing.T) {
	conn, err := dns.DialTimeout("udp", dnsToGrpcAddr, 1*time.Second)
	if err != nil {
		t.Fatalf("dns.Dial error: %v", err)
	}
	defer conn.Close()

	err = dnsQuery(conn)
	if err != nil {
		t.Errorf("dns query returned error: %v", err)
	}
}

//
// === Benchmarks ===
//

func manyDNSQueries(b *testing.B, addr string) {
	conn, err := dns.DialTimeout("udp", addr, 1*time.Second)
	if err != nil {
		b.Fatalf("dns.Dial error: %v", err)
	}
	defer conn.Close()

	for i := 0; i < b.N; i++ {
		err = dnsQuery(conn)
		if err != nil {
			b.Errorf("dns query returned error: %v", err)
		}
	}
}

func BenchmarkDirect(b *testing.B) {
	manyDNSQueries(b, dnsSrvAddr)
}

func BenchmarkWithProxy(b *testing.B) {
	manyDNSQueries(b, dnsToGrpcAddr)
}

//
// === Test environment ===
//

// dnsServer implements a DNS server for testing.
// It always gives the same reply, regardless of the query.
type dnsServer struct {
	Addr     string
	srv      *dns.Server
	answerRR dns.RR
}

func (s *dnsServer) Handler(w dns.ResponseWriter, r *dns.Msg) {
	// Building the reply (and setting the corresponding id) is cheaper than
	// copying a "master" message.
	m := &dns.Msg{}
	m.Id = r.Id
	m.Response = true
	m.Authoritative = true
	m.Rcode = dns.RcodeSuccess
	m.Answer = append(m.Answer, s.answerRR)
	w.WriteMsg(m)
}

func (s *dnsServer) ListenAndServe() {
	var err error

	s.answerRR, err = dns.NewRR("test.blah A 1.2.3.4")
	if err != nil {
		panic(err)
	}

	s.srv = &dns.Server{
		Addr:    s.Addr,
		Net:     "udp",
		Handler: dns.HandlerFunc(s.Handler),
	}
	err = s.srv.ListenAndServe()
	if err != nil {
		panic(err)
	}
}

// generateCert generates a new, INSECURE self-signed certificate and writes
// it to a pair of (cert.pem, key.pem) files to the given path.
// Note the certificate is only useful for testing purposes.
func generateCert(path string) error {
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1234),
		Subject: pkix.Name{
			Organization: []string{"dnss testing"},
		},

		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},

		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(30 * time.Minute),

		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature |
			x509.KeyUsageCertSign,

		BasicConstraintsValid: true,
		IsCA: true,
	}

	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return err
	}

	derBytes, err := x509.CreateCertificate(
		rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	certOut, err := os.Create(path + "/cert.pem")
	if err != nil {
		return err
	}
	defer certOut.Close()
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyOut, err := os.OpenFile(
		path+"/key.pem", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyOut.Close()

	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	}
	pem.Encode(keyOut, block)
	return nil
}

// waitForServers waits 5 seconds for the servers to start, and returns an
// error if they fail to do so.
// It does this by repeatedly querying the dns-to-grpc server until it either
// replies or times out. Note we do not do any validation of the reply.
func waitForServers() error {
	conn, err := dns.DialTimeout("udp", dnsToGrpcAddr, 1*time.Second)
	if err != nil {
		return fmt.Errorf("dns.Dial error: %v", err)
	}
	defer conn.Close()

	after := time.After(5 * time.Second)
	tick := time.Tick(100 * time.Millisecond)
	select {
	case <-after:
		return fmt.Errorf("timed out")
	case <-tick:
		conn.SetDeadline(time.Now().Add(1 * time.Second))
		err := dnsQuery(conn)
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("not reachable")
}

// realMain is the real main function, which returns the value to pass to
// os.Exit(). We have to do this so we can use defer.
func realMain(m *testing.M) int {
	flag.Parse()
	defer glog.Flush()

	// Generate certificates in a temporary directory.
	tmpDir, err := ioutil.TempDir("", "dnss_test:")
	if err != nil {
		fmt.Printf("Failed to create temp dir: %v\n", tmpDir)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	err = generateCert(tmpDir)
	if err != nil {
		fmt.Printf("Failed to generate cert for testing: %v\n", err)
		return 1
	}

	// DNS to GRPC server.
	gr := dnstox.NewGRPCResolver(grpcToDnsAddr, tmpDir+"/cert.pem")
	cr := dnstox.NewCachingResolver(gr)
	dtg := dnstox.New(dnsToGrpcAddr, cr, "")
	go dtg.ListenAndServe()

	// GRPC to DNS server.
	gtd := &grpctodns.Server{
		Addr:     grpcToDnsAddr,
		Upstream: dnsSrvAddr,
		CertFile: tmpDir + "/cert.pem",
		KeyFile:  tmpDir + "/key.pem",
	}
	go gtd.ListenAndServe()

	// DNS test server.
	dnsSrv := dnsServer{
		Addr: dnsSrvAddr,
	}
	go dnsSrv.ListenAndServe()

	// Wait for the servers to start up.
	err = waitForServers()
	if err != nil {
		fmt.Printf("Error waiting for the test servers to start: %v\n", err)
		fmt.Printf("Check the INFO logs for more details\n")
		return 1
	}

	return m.Run()
}

func TestMain(m *testing.M) {
	os.Exit(realMain(m))
}
