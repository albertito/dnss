
# dnss

dnss is a tool for encapsulating DNS over HTTPS.


## Quick start

If you're using Debian or Ubuntu, `apt install dnss` will install a dnss
instance already configured in DNS-over-HTTPS mode and using
https://dns.google.com as a server.


To do the same manually:

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


## DNS to HTTPS proxy

dnss can act as a DNS-to-HTTPS proxy, using https://dns.google.com as a
server, or anything implementing the same API, which is documented at
https://developers.google.com/speed/public-dns/docs/dns-over-https (note it's
in beta and subject to changes).

```
+--------+       +----------------+        +----------------+
|        |       |     dnss       |        |                |
| client +-------> (dns-to-https) +--------> dns.google.com |
|        |  DNS  |                |        |                |
+--------+  UDP  +----------------+  HTTP  +----------------+
                                     SSL
                                     TCP
```


## HTTPS to DNS proxy

dnss can also act as an HTTPS-to-DNS proxy, implementing the HTTP-based API
documented at
https://developers.google.com/speed/public-dns/docs/dns-over-https (note it's
in beta and subject to changes).

You can use this instead of https://dns.google.com if you want more control
over the servers and the final DNS server used (for example if you are in an
isolated environment, such as a test lab or a private network).


## Alternatives

https://dnscrypt.org/ is a great, more end-to-end alternative to dnss.

