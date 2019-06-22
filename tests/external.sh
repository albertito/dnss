#!/bin/bash
#
# Integration tests against external hosts.
#
# The goal is to test how dnss interacts with publicly available services.
#
# These tests use the network and public internet to talk to:
# - the machine's configured DNS server
# - dns.google.com
# - 1.1.1.1.
#
# So the tests are not hermetic and could fail for external reasons.


set -e

# Set traps to kill our subprocesses when we exit (for any reason).
trap ":" TERM      # Avoid the EXIT handler from killing bash.
trap "exit 2" INT  # Ctrl-C, make sure we fail in that case.
trap "kill 0" EXIT # Kill children on exit.

# The tests are run from the repository root.
cd "$(realpath `dirname ${0}`)/../"

# Build the dnss binary.
if [ "$COVER_DIR" != "" ]; then
	go test -covermode=count -coverpkg=./... -c -tags coveragebin
	mv dnss.test dnss
else
	go build
fi


# Run dnss in the background (sets $PID to its process id).
function dnss() {
	# Set the coverage arguments each time, as we don't want the different
	# runs to override the generated profile.
	if [ "$COVER_DIR" != "" ]; then
		COVER_ARGS="-test.run=^TestRunMain$ \
			-test.coverprofile=$COVER_DIR/it-`date +%s.%N`.out"
	fi

	$SYSTEMD_ACTIVATE ./dnss $COVER_ARGS \
		-v 3 -monitoring_listen_addr :1900 \
		"$@" > .dnss.log 2>&1 &
	PID=$!
}

# Wait until there's something listening on the given port.
function wait_until_ready() {
	PROTO=$1
	PORT=$2

	while ! bash -c "true < /dev/$PROTO/localhost/$PORT" 2>/dev/null ; do
		sleep 0.01
	done
}

# Resolve some queries.
function resolve() {
	wait_until_ready tcp 1053

	kdig @127.0.0.1:1053 +tcp  example.com a > .dig.log
	if ! grep -E -i -q '^example.com.*A'  .dig.log; then
		echo "----- FAILED"
		cat .dig.log
		false
	fi

	kdig @127.0.0.1:1053 +notcp  example.com a > .dig.log
	if ! grep -E -i -q '^example.com.*A'  .dig.log; then
		echo "----- FAILED"
		cat .dig.log
		false
	fi

	kdig @127.0.0.1:1053 +notcp  com.ar NS > .dig.log
	if ! grep -E -i -q '^com.ar.*NS'  .dig.log; then
		echo "----- FAILED"
		cat .dig.log
		false
	fi
}

# HTTP GET, using wget.
function get() {
	URL=$1

	wget -O.wget.out $URL > .wget.log 2>&1
}

echo "## Misc"
# Missing arguments (expect it to fail).
dnss
if wait $PID; then
	echo "Expected dnss to fail, but it worked"
	exit 1
fi


echo "## Launching HTTPS server"
dnss -enable_https_to_dns \
	-testing__insecure_http -https_server_addr "localhost:1999"
HTTP_PID=$PID
mv .dnss.log .dnss.http.log

wait_until_ready tcp 1999

echo "## Checking /debug/flags"
if ! get "http://localhost:1900/debug/flags"; then
	echo "Failed to get /debug/flags"
	exit 1
fi
if ! grep -q "testing__insecure_http=true" .wget.out; then
	echo "/debug/flags did not contain expected flags (see .wget.out)"
	exit 1
fi

echo "## Autodetect against dnss"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-testing__insecure_http \
	-https_upstream "http://localhost:1999/dns-query"

resolve
kill $PID

echo "## JSON against dnss"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-testing__insecure_http \
	-force_mode="JSON" \
	-https_upstream "http://localhost:1999/dns-query"

resolve

# Exercise some interesting JSON requests.
get "http://localhost:1999/dns-query?name=test&edns_client_subnet=1.2.3.4/24"
get "http://localhost:1999/dns-query?name=test&edns_client_subnet=2001:700:300::/48"
if get "http://localhost:1999/dns-query?name=test&type=lalala"; then
	echo "GET with invalid query did not fail"
	exit 1
fi

kill $PID

echo "## DoH against dnss"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-testing__insecure_http \
	-force_mode="DoH" \
	-https_upstream "http://localhost:1999/dns-query"

# Exercise DoH via GET (dnss always uses POST).
get "http://localhost:1999/resolve?&dns=q80BAAABAAAAAAAAA3d3dwdleGFtcGxlA2NvbQAAAQAB"

if get "http://localhost:1999/resolve?&dns=invalidbase64@"; then
	echo "GET with invalid base64 did not fail"
	exit 1
fi

if get "http://localhost:1999/resolve?nothing"; then
	echo "GET with nonsense query did not fail"
	exit 1
fi

resolve
kill $PID

kill $HTTP_PID


echo "## Autodetect against dns.google.com"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-https_upstream "https://dns.google.com/resolve"

resolve
kill $PID

echo "## JSON against dns.google.com"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-force_mode="JSON" \
	-https_upstream "https://dns.google.com/resolve"

resolve
kill $PID


# DoH integration test against some publicly available servers.
# https://github.com/curl/curl/wiki/DNS-over-HTTPS#publicly-available-servers
# Note not all of the ones in the list are actually functional.
for server in \
	"https://1.1.1.1/dns-query" \
	"https://cloudflare-dns.com/dns-query" \
	"https://dns.google.com/experimental" \
	"https://dns.quad9.net/dns-query" \
	"https://doh.cleanbrowsing.org/doh/family-filter/" \
	"https://doh.powerdns.org" \
	"https://doh.crypto.sx/dns-query" \
	"https://doh.securedns.eu/dns-query" \
	;
do
	echo "## DoH against $server"
	dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
		-force_mode="DoH" \
		-https_upstream "$server"
	resolve
	kill $PID

	echo "## Autodetect against $server"
	dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
		-https_upstream "$server"
	resolve
	kill $PID
done


echo "## Defaults"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053"

resolve

# Take this opportunity to query some URLs, to exercise their code when they
# have requests.
get http://localhost:1900/debug/dnsserver/cache/dump
get http://localhost:1900/debug/dnsserver/cache/flush

kill $PID


# systemd socket activation tests must check one protocol at a time, due to
# systemd-socket-activate not being able to listen on both.
echo "## Socket activation via systemd: TCP"
SYSTEMD_ACTIVATE="systemd-socket-activate -l 1053"
dnss -enable_dns_to_https -dns_listen_addr "systemd"

wait_until_ready tcp 1053
kdig @127.0.0.1:1053 +tcp  example.com a > .dig.log
grep -E -q '^example.com.*A'  .dig.log

kill $PID

echo "## Socket activation via systemd: UDP"
SYSTEMD_ACTIVATE="systemd-socket-activate -d -l 1053"
dnss -enable_dns_to_https -dns_listen_addr "systemd"

kdig @127.0.0.1:1053 +notcp  example.com a > .dig.log
grep -E -q '^example.com.*A'  .dig.log

kill $PID


echo SUCCESS
