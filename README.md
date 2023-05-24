
# dnss

dnss is a daemon for using DNS over HTTPS.

It can act as a proxy, receiving DNS requests and resolving them using
DNS-over-HTTPs (DoH). This can be useful to improve DNS security and privacy
on laptops and small/home networks.

It can also act as a DoH server, in case you want end to end control.

[![Tests status](https://github.com/albertito/dnss/actions/workflows/tests.yaml/badge.svg?branch=master)](https://github.com/albertito/dnss/actions)
[![Go Report Card](https://goreportcard.com/badge/github.com/albertito/dnss)](https://goreportcard.com/report/github.com/albertito/dnss)


## Features

* Supports the
  [DNS Queries over HTTPS (DoH)](https://en.wikipedia.org/wiki/DNS_over_HTTPS)
  standard ([RFC 8484](https://tools.ietf.org/html/rfc8484)).
* Local cache (optional).
* HTTP(s) proxy support, autodetected from the environment.
* Monitoring HTTP server, with exported variables and tracing to help
  debugging.
* Separate resolution for specific domains, useful for home networks with
  local DNS servers.


## Install

### Debian/Ubuntu

The `dnss` package installs the daemon configured in proxy mode and ready to
use, using Google's public resolvers (and easily changed via configuration).

```shell
sudo apt install dnss
```


### Manual install

To download and build the binary:

```shell
go install blitiri.com.ar/go/dnss
```

And if you want to configure the daemon to be automatically run by systemd:

```shell
# Copy the binary to a system-wide location.
sudo cp "$GOPATH/bin/dnss" /usr/local/bin/

# Set it up in systemd.
sudo cp "$GOPATH"/src/blitiri.com.ar/go/dnss/etc/systemd/dns-to-https/* \
	/etc/systemd/system/

sudo systemctl dnss enable
```


## Examples

### DNS server (proxy mode)

Listens on port 53 for DNS queries, resolves them using the given HTTPS URL.

```shell
# Use the default HTTPS URL (currently, dns.google):
dnss -enable_dns_to_https

# Use Cloudflare's 1.1.1.1:
dnss -enable_dns_to_https -https_upstream="https://1.1.1.1/dns-query"

# Use Google's dns.google:
dnss -enable_dns_to_https -https_upstream="https://dns.google/dns-query"

# Use the default HTTPS URL for all resolutions, except for domain "myhome"
# which is resolved via a local DNS server.
dnss -enable_dns_to_https -dns_server_for_domain="myhome:10.0.1.1:53"
```

### HTTPS server

Receives DNS over HTTPS requests, resolves them using the machine's configured
DNS servers, and returns the replies.  You will need to have certificates for
the domains you want to serve.

Supports both DoH and JSON modes automatically, and the endpoints are
`/dns-query` and `/resolve`.

```shell
# Serve DNS over HTTPS requests, take certificates from letsencrypt.
DOMAIN=yourdomain.com
dnss -enable_https_to_dns \
  -https_key=/etc/letsencrypt/live/$DOMAIN/privkey.pem \
  -https_cert=/etc/letsencrypt/live/$DOMAIN/fullchain.pem
```

