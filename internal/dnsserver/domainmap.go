package dnsserver

import (
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

// DomainMap maps a DNS name to an arbitrary string.
type DomainMap map[string]string

// Set the value for the given domain.
func (m DomainMap) Set(domain, value string) {
	m[dns.CanonicalName(domain)] = value
}

// GetExact value for the given domain, using an exact lookup (the domain must
// match exactly what was set).
func (m DomainMap) GetExact(domain string) (string, bool) {
	v, ok := m[dns.CanonicalName(domain)]
	return v, ok
}

// GetMostSpecific value for the given domain, using a most-specific lookup
// (we pick the map entry that is closest to the domain).
func (m DomainMap) GetMostSpecific(domain string) (string, bool) {
	domain = dns.CanonicalName(domain)
	mc := 0
	mv := ""
	ok := false
	for d, v := range m {
		if !dns.IsSubDomain(d, domain) {
			continue
		}

		// Keep the match with the most labels (the most specific).
		c := dns.CountLabel(d)
		if c > mc {
			mc = c
			mv = v
			ok = true
		}
	}

	return mv, ok
}

// DomainMapFromString takes a string in the form of
// "domain1:addr1,domain2:addr2,..." and returns a dnsserver.DomainMap like
// {"domain1": "addr1", "domain2": "addr2", ...}.
func DomainMapFromString(s string) (DomainMap, error) {
	m := DomainMap{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		xs := strings.SplitN(pair, ":", 2)
		if len(xs) != 2 {
			return nil, fmt.Errorf("%q: %w", pair, errInvalidFormat)
		}
		m.Set(strings.TrimSpace(xs[0]), strings.TrimSpace(xs[1]))
	}
	return m, nil
}

var errInvalidFormat = fmt.Errorf("entry does not have a ':'")
