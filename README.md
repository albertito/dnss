
# dnss

dnss is a tool for encapsulating DNS over more secure protocols, like HTTPS or
GRPC.

## Quick start

If you want to set up dnss quickly, in DNS-over-HTTPS mode and using
https://dns.google.com as a server, you can run the following:

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


## DNS over HTTPS

dnss can act as a DNS-over-HTTPS proxy, using https://dns.google.com as a
server.

```
+--------+       +----------------+        +----------------+
|        |       |     dnss       |        |                |
| client +-------> (dns-to-https) +--------> dns.google.com |
|        |  DNS  |                |        |                |
+--------+  UDP  +----------------+  HTTP  +----------------+
                                     SSL
                                     TCP
```


## DNS over GRPC

dnss can encapsulate DNS over GRPC.

It can be useful when you want to use a particular DNS server, but don't want
some parts of the network in between to be able to see your traffic.


```
+--------+       +---------------+        +---------------+      +------------+
|        |       |     dnss      |        |     dnss      |      |            |
| client +-------> (dns-to-grpc) +--------> (grpc-to-dns) +------> DNS server |
|        |  DNS  |               |  DNS   |               | DNS  |            |
+--------+  UDP  +---------------+  GRPC  +---------------+ UDP  +------------+
                                    SSL
                                    TCP
```

In "dns-to-grpc" mode, it listens to DNS requests and pass them on to a server
using GRPC. It also has a small cache.

In "grpc-to-dns" mode, it receives DNS requests via GRPC, and resolves them
using a normal, fixed DNS server.


## Alternatives

https://dnscrypt.org/ is a great, more end-to-end alternative to dnss.

