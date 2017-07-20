
# dnss

dnss is a tool for encapsulating DNS over HTTPS.

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


## Alternatives

https://dnscrypt.org/ is a great, more end-to-end alternative to dnss.

