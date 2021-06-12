#!/bin/bash
#
# Integration tests against external hosts.
#
# The goal is to test how dnss interacts with publicly available services.
#
# These tests use the network and public internet to talk to the machine's
# configured DNS server, and various public resolvers.
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

# Run minidns in the background (sets $MINIDNS_PID to its process id).
function minidns() {
	go run tests/minidns.go \
		-addr ":1953" \
		-zones tests/testzones \
		> .minidns.log 2>&1 &
	MINIDNS_PID=$!
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

	# The response exceeds the default UDP size (512b), so it should fall back
	# to TCP. This exercises the truncating logic.
	kdig @127.0.0.1:1053  google.com TXT > .dig.log 2>&1
	if ! grep -E -i -q '^google.com.*TXT'  .dig.log; then
		echo "----- FAILED (missing response)"
		cat .dig.log
		false
	fi
	if ! grep -E -i -q 'retrying over TCP'  .dig.log; then
		echo "----- FAILED (did not use TCP)"
		cat .dig.log
		false
	fi

	# Same as above, but we explicitly are ok with a 2k response, which
	# fits the reply. We check that we do NOT fall back to TCP.
	kdig @127.0.0.1:1053 +bufsize=2048 google.com TXT > .dig.log 2>&1
	if ! grep -E -i -q '^google.com.*TXT'  .dig.log; then
		echo "----- FAILED (missing response)"
		cat .dig.log
		false
	fi
	if grep -E -i -q 'retrying over TCP'  .dig.log; then
		echo "----- FAILED (used TCP)"
		cat .dig.log
		false
	fi
}

# HTTP GET, using wget.
function get() {
	URL=$1

	wget -O.wget.out $URL > .wget.log 2>&1
}

function generate_certs() {
	mkdir -p .certs/$1
	(
		cd .certs/$1
		go run ../../tests/generate_cert.go \
			-ca -duration=1h --host=$1
	)
}


echo "## Misc"
# Missing arguments (expect it to fail).
dnss
if wait $PID; then
	echo "Expected dnss to fail, but it worked"
	exit 1
fi

echo "## Launching minidns for testing"
minidns
wait_until_ready tcp 1953

echo "## Launching HTTPS server"
dnss -enable_https_to_dns -dns_upstream "localhost:1953" \
	-insecure_http_server -https_server_addr "localhost:1999"
HTTP_PID=$PID
mv .dnss.log .dnss.http.log

wait_until_ready tcp 1999

echo "## Checking /debug/flags"
if ! get "http://localhost:1900/debug/flags"; then
	echo "Failed to get /debug/flags"
	exit 1
fi
if ! grep -q "insecure_http_server=true" .wget.out; then
	echo "/debug/flags did not contain expected flags (see .wget.out)"
	exit 1
fi

echo "## DoH against dnss"
dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-fallback_upstream "127.0.0.1:1953" \
	-https_upstream "http://upstream:1999/dns-query"

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


echo "## HTTPS with custom certificates"
generate_certs upstream
dnss -enable_https_to_dns \
	-https_key .certs/upstream/privkey.pem \
	-https_cert .certs/upstream/fullchain.pem \
	-https_server_addr "localhost:1999"
HTTP_PID=$PID
mv .dnss.log .dnss.http.log
wait_until_ready tcp 1999

dnss -enable_dns_to_https -dns_listen_addr "localhost:1053" \
	-fallback_upstream "127.0.0.1:1953" \
	-https_client_cafile .certs/upstream/fullchain.pem \
	-https_upstream "https://upstream:1999/dns-query"

resolve

kill $PID
kill $HTTP_PID
kill $MINIDNS_PID


# DoH integration test against some publicly available servers.
# https://github.com/curl/curl/wiki/DNS-over-HTTPS#publicly-available-servers
# Note not all of the ones in the list are actually functional.
for server in \
	"https://1.1.1.1/dns-query" \
	"https://cloudflare-dns.com/dns-query" \
	"https://dns.google/dns-query" \
	"https://dns.quad9.net/dns-query" \
	"https://doh.powerdns.org" \
	;
do
	echo "## DoH against $server"
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

sleep 0.2
kdig @127.0.0.1:1053 +notcp  example.com a > .dig.log
grep -E -q '^example.com.*A'  .dig.log

kill $PID


echo SUCCESS
