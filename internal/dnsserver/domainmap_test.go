package dnsserver

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDomainMap(t *testing.T) {
	m := DomainMap{}
	m.Set("a.com", "valuex")
	m.Set("a.com", "valueA")
	m.Set("x.A.com", "valueX")
	m.Set("y.a.com", "valueY")

	type tcase struct {
		req string
		val string
		ok  bool
	}

	cases := []tcase{
		{"a.com", "valueA", true},
		{"A.cOm", "valueA", true},
		{"A.COM.", "valueA", true},
		{"x.a.com", "valueX", true},
		{"y.a.com", "valueY", true},
		{"com", "", false},
		{"b.a.com", "", false},
	}
	for i, c := range cases {
		val, ok := m.GetExact(c.req)
		if val != c.val || ok != c.ok {
			t.Errorf("case %d: GetExact(%q) expected (%q, %v), got (%q, %v)",
				i, c.req, c.val, c.ok, val, ok)
		}
	}

	cases = []tcase{
		{"a.com", "valueA", true},
		{"x.a.com", "valueX", true},
		{"y.a.com", "valueY", true},
		{"b.a.com", "valueA", true},
		{"z.x.a.com", "valueX", true},
		{"com", "", false},
	}
	for i, c := range cases {
		val, ok := m.GetMostSpecific(c.req)
		if val != c.val || ok != c.ok {
			t.Errorf("case %d: GetMostSpecific(%q) expected (%q, %v), got (%q, %v)",
				i, c.req, c.val, c.ok, val, ok)
		}
	}
}

func TestDomainMapFromString(t *testing.T) {
	cases := []struct {
		s   string
		m   DomainMap
		err error
	}{
		{"", DomainMap{}, nil},
		{"d1:1.1.1.1:1111", DomainMap{"d1.": "1.1.1.1:1111"}, nil},
		{"Do-Main:1.1.1.1:1111", DomainMap{"do-main.": "1.1.1.1:1111"}, nil},
		{
			"d1:1.1.1.1:1111, d2.: 2.2.2.2:2222 ,,d3 : 3.3.3.3:3333, d4:",
			DomainMap{
				"d1.": "1.1.1.1:1111",
				"d2.": "2.2.2.2:2222",
				"d3.": "3.3.3.3:3333",
				"d4.": "",
			},
			nil,
		},
		{"abc", nil, errInvalidFormat},
		{"abc:def,xyz", nil, errInvalidFormat},
	}
	for i, c := range cases {
		m, err := DomainMapFromString(c.s)
		if diff := cmp.Diff(c.m, m); diff != "" {
			t.Errorf("%d: DomainMapFromString(%q) mismatch (-want +got):\n%s", i, c.s, diff)
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%d: DomainMapFromString(%q) unexpected error: "+
				"want:%q ; got:%q", i, c.s, c.err, err)
		}
	}
}
