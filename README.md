
# dnss

dnss is a daemon for using DNS over HTTPS.

It can act as a "DNS to HTTPS" proxy (the most common use case), and as a "DNS
over HTTPS" server in case you want end to end control.


## Install

### Debian/Ubuntu

`$ apt install dnss`

That installs a dnss instance already configured in proxy mode and ready to
go, using Google's public resolvers (and easily changed via configuration).


### Manual install

```
# If you have Go installed but no environment prepared, do:
mkdir /tmp/dnss; export GOPATH=/tmp/dnss; cd $GOPATH

# Download and build the binary.
go get blitiri.com.ar/go/dnss

# Copy the binary to a system-wide location.
sudo cp $GOPATH/bin/dnss /usr/local/bin

# Set it up in systemd.
sudo cp $GOPATH/src/blitiri.com.ar/go/dnss/etc/systemd/dns-to-https/* \
	/etc/systemd/system/

sudo systemctl dnss enable
```

## Supported protocols

dnss supports the following encapsulation protocols, both in proxy and in
server modes:

* JSON-based, as implemented by [dns.google.com](https://dns.google.com)
  ([reference](https://developers.google.com/speed/public-dns/docs/dns-over-https)).
* [DNS Queries over HTTPS
  (DoH)](https://tools.ietf.org/html/draft-ietf-doh-dns-over-https) proposed
  standard (and implemented by [Cloudflare's 1.1.1.1](https://1.1.1.1/)).

