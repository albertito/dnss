// Tests for the query parsing.
package httpstodns

import (
	"net"
	"net/url"
	"reflect"
	"testing"

	"github.com/miekg/dns"
)

func makeURL(t *testing.T, query string) *url.URL {
	u, err := url.Parse("http://site/resolve?" + query)
	if err != nil {
		t.Fatalf("URL parsing failed: %v", err)
	}

	return u
}

func makeIPNet(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func queryEq(q1, q2 query) bool {
	return reflect.DeepEqual(q1, q2)
}

// A DNS name which is too long (> 253 characters), but otherwise valid.
const longName = "pablitoclavounclavitoqueclavitoclavopablito-pablitoclavounclavitoqueclavitoclavopablito-pablitoclavounclavitoqueclavitoclavopablito-pablitoclavounclavitoqueclavitoclavopablito-pablitoclavounclavitoqueclavitoclavopablito-pablitoclavounclavitoqueclavitoclavopablito"

func Test(t *testing.T) {
	cases := []struct {
		rawQ string
		q    query
	}{
		{"name=hola", query{"hola", dns.TypeA, false, nil}},
		{"name=hola&type=a", query{"hola", dns.TypeA, false, nil}},
		{"name=hola&type=A", query{"hola", dns.TypeA, false, nil}},
		{"name=hola&type=1", query{"hola", dns.TypeA, false, nil}},
		{"name=hola&type=MX", query{"hola", dns.TypeMX, false, nil}},
		{"name=hola&type=txt", query{"hola", dns.TypeTXT, false, nil}},
		{"name=x&cd", query{"x", dns.TypeA, true, nil}},
		{"name=x&cd=1", query{"x", dns.TypeA, true, nil}},
		{"name=x&cd=true", query{"x", dns.TypeA, true, nil}},
		{"name=x&cd=0", query{"x", dns.TypeA, false, nil}},
		{"name=x&cd=false", query{"x", dns.TypeA, false, nil}},
		{"name=x&type=mx;cd", query{"x", dns.TypeMX, true, nil}},

		{
			"name=x&edns_client_subnet=1.2.3.0/21",
			query{"x", dns.TypeA, false, makeIPNet("1.2.3.0/21")},
		},
		{
			"name=x&edns_client_subnet=2001:700:300::/48",
			query{"x", dns.TypeA, false, makeIPNet("2001:700:300::/48")},
		},
		{
			"name=x&type=mx&cd&edns_client_subnet=2001:700:300::/48",
			query{"x", dns.TypeMX, true, makeIPNet("2001:700:300::/48")},
		},
	}
	for _, c := range cases {
		q, err := parseQuery(makeURL(t, c.rawQ))
		if err != nil {
			t.Errorf("query %q: error %v", c.rawQ, err)
		}
		if !queryEq(q, c.q) {
			t.Errorf("query %q: expected %v, got %v", c.rawQ, c.q, q)
		}
	}

	errCases := []struct {
		raw string
		err error
	}{
		{"", emptyNameErr},
		{"name=" + longName, nameTooLongErr},
		{"name=x;type=0", intOutOfRangeErr},
		{"name=x;type=-1", intOutOfRangeErr},
		{"name=x;type=65536", unknownType},
		{"name=x;type=merienda", unknownType},
		{"name=x;cd=lala", invalidCD},
		{"name=x;edns_client_subnet=lala", invalidSubnetErr},
		{"name=x;edns_client_subnet=1.2.3.4", invalidSubnetErr},
	}
	for _, c := range errCases {
		_, err := parseQuery(makeURL(t, c.raw))
		if err != c.err {
			t.Errorf("query %q: expected error %v, got %v", c.raw, c.err, err)
		}
	}
}
