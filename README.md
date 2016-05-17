
# dnss

dnss is a tool for encapsulating DNS over more secure protocols.


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

