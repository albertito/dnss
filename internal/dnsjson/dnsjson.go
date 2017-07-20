// Package dnsjson contains structures for representing DNS responses as JSON.
//
// Matches the API implemented by https://dns.google.com/.
package dnsjson

type Response struct {
	Status   int  // Standard DNS response code (32 bit integer).
	TC       bool // Whether the response is truncated
	RD       bool // Whether recursion is desired.
	RA       bool // Whether recursion is available.
	AD       bool // Whether all response data was validated with DNSSEC
	CD       bool // Whether the client asked to disable DNSSEC
	Question []RR // Question we're responding to.
	Answer   []RR // Answer to the question.
}

type RR struct {
	Name string `json:name`
	Type uint16 `json:type`
	TTL  uint32 `json:TTL`
	Data string `json:data`
}
