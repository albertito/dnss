// Package dnsjson contains structures for representing DNS responses as JSON.
//
// Matches the API implemented by https://dns.google.com/.
package dnsjson

// Response is the highest level struct in the DNS JSON response.
// Note the fields must match the JSON API specified at
// https://developers.google.com/speed/public-dns/docs/dns-over-https#dns_response_in_json/.
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

// RR represents a JSON-encoded DNS RR.
// Note the fields must match the JSON API specified at
// https://developers.google.com/speed/public-dns/docs/dns-over-https#dns_response_in_json/.
type RR struct {
	Name string // FQDN for the RR.
	Type uint16 // DNS RR type.
	TTL  uint32 // Record's time to live, in seconds.
	Data string // Data for the record (e.g. for A it's the IP address).
}
