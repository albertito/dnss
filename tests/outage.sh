#!/bin/bash
#
# Tests how dnss behaves when the network goes away, and then comes back.
#
# It runs dnss (in DNS-to-HTTPS mode) in a simulated network, and while
# continuously sending queries to it, it introduces different kinds of
# outages (e.g. the link going down, or the upstream becoming unreachable).
# Then it restores the network, and measures how long it takes for dnss to
# successfully resolve queries again.
#
# The network is simulated using Linux network namespaces, inside an
# unprivileged user namespace, so it doesn't require root.
#
#   client ns               router ns                 upstream ns
#   dnss (DNS-to-HTTPS)     forwards between          dnss (HTTPS-to-DNS)
#   dnsprobe                both links                minidns
#      c0 -------------------- r0      r1 -------------------- u0
#   10.0.1.2              10.0.1.1     10.0.2.1             10.0.2.2
#
# Usage:
#   tests/outage.sh [scenario...]
#
# Scenarios (default: all of them):
#   linkdown          The client's link goes down (e.g. wifi disconnects).
#   blackhole         The router silently drops traffic to the upstream (e.g.
#                     an ISP outage, the local network is still up).
#   ipchange          The client's address goes away, and comes back as a
#                     different one (e.g. switching networks or VPN, or NAT
#                     state loss). Existing connections are dead.
#   upstream-restart  The upstream DoH server goes down and comes back.
#   start-offline     dnss starts with the client's link down.
#   powercut          dnss starts with the client's link down. Half way
#                     through the outage, the link comes up but the upstream
#                     is still unreachable (e.g. after a power cut, the local
#                     router comes up before its internet connection does).
#
# Environment variables:
#   DNSS_BIN        dnss binary to test (default: build from the tree).
#   DURATIONS       Outage durations in seconds (default: "5 60").
#   RECOVERY_LIMIT  Seconds we allow for recovery (default: 30).
#   DNSS_FLAGS      Extra flags to pass to the dnss being tested.
#   DNSS_ENV        Extra environment for the dnss being tested (e.g.
#                   "GODEBUG=http2debug=1").
#   PROBE_INTERVAL  Time between queries sent to dnss (default: 200ms).
#   STABLE_COUNT    Number of consecutive successful queries needed to
#                   consider dnss recovered (default: 10).
#   UPSTREAM        How dnss refers to the upstream: "name" (default) uses a
#                   hostname, which needs to be resolved via the fallback
#                   resolver; "ip" uses the IP address directly.
#   OUTAGE_DIR      Working directory (default: .outage). Using different
#                   ones allows running multiple instances in parallel.
#
# Results are printed at the end. Logs for each run are left in
# $OUTAGE_DIR/runs/<scenario>-<duration>/.

set -e

# The tests are run from the repository root.
cd "$(realpath "$(dirname "$0")/../" )"

ALL_SCENARIOS="linkdown blackhole ipchange upstream-restart start-offline
	powercut"
DURATIONS="${DURATIONS:-5 60}"
RECOVERY_LIMIT="${RECOVERY_LIMIT:-30}"
PROBE_INTERVAL="${PROBE_INTERVAL:-200ms}"
STABLE_COUNT="${STABLE_COUNT:-10}"
UPSTREAM="${UPSTREAM:-name}"

W="${OUTAGE_DIR:-.outage}"

#####################################
# Outer stage: build everything, then re-run ourselves inside the namespaces.
# We need to build outside, as there is no network access inside.

if [ "$OUTAGE_INSIDE_NS" != "1" ]; then
	rm -rf "$W"
	mkdir -p "$W/certs"

	echo "## Building"
	go build -o "$W/dnss" .
	go build -o "$W/minidns" tests/minidns.go
	go build -o "$W/dnsprobe" tests/dnsprobe.go

	if [ "$DNSS_BIN" == "" ]; then
		DNSS_BIN="$W/dnss"
	fi
	DNSS_BIN="$(realpath "$DNSS_BIN")"

	GENERATE_CERT="$PWD/tests/generate_cert.go"
	(
		cd "$W/certs"
		go run "$GENERATE_CERT" -ca -duration=24h \
			--host=upstream.test,10.0.2.2
	) > /dev/null

	export OUTAGE_INSIDE_NS=1 DNSS_BIN
	exec unshare --user --map-root-user --net "$0" "$@"
fi

#####################################
# Inner stage: we are root in our own user and network namespaces.
# This (initial) network namespace is the client's.

# Kill all our background processes on exit.
# Note we can't use "kill 0", as we may share the process group with our
# caller. Because of this, background processes must be started directly
# (and not via shell functions), so they are tracked as jobs.
trap "exit 2" INT TERM
trap 'kill $(jobs -p) $ROUTER_NS $UPSTREAM_NS 2> /dev/null' EXIT

SCENARIOS="${*:-$ALL_SCENARIOS}"

# Current time, in unix milliseconds.
function now() {
	date +%s%3N
}

function log() {
	echo "$(date +%T.%3N)  $*"
}

# Create a new network namespace, and print the pid of a process inside it,
# so we can refer to it.
# The process' output must be redirected, otherwise the caller's command
# substitution would wait for it to finish.
function new_ns() {
	unshare --net sleep infinity > /dev/null 2>&1 &
	echo $!
}

ROUTER_NS=$(new_ns)
UPSTREAM_NS=$(new_ns)

# Run a command in the router or upstream namespaces.
function inr() { nsenter -t "$ROUTER_NS" -n "$@"; }
function inu() { nsenter -t "$UPSTREAM_NS" -n "$@"; }

CLIENT_IP=10.0.1.2

function setup_network() {
	ip link set lo up
	inr ip link set lo up
	inu ip link set lo up

	# client <-> router.
	ip link add c0 type veth peer name r0
	ip link set r0 netns "$ROUTER_NS"
	ip addr add $CLIENT_IP/24 dev c0
	ip link set c0 up
	ip route add default via 10.0.1.1
	inr ip addr add 10.0.1.1/24 dev r0
	inr ip link set r0 up

	# router <-> upstream.
	inr ip link add r1 type veth peer name u0
	inr ip link set u0 netns "$UPSTREAM_NS"
	inr ip addr add 10.0.2.1/24 dev r1
	inr ip link set r1 up
	inu ip addr add 10.0.2.2/24 dev u0
	inu ip link set u0 up
	inu ip route add default via 10.0.2.1

	inr sh -c "echo 1 > /proc/sys/net/ipv4/ip_forward"
}

function start_minidns() {
	cat > $W/zones <<EOF
upstream.test A 10.0.2.2
EOF
	nsenter -t "$UPSTREAM_NS" -n \
		$W/minidns -addr ":53" -zones $W/zones > $W/minidns.log 2>&1 &
}

function start_upstream_dnss() {
	nsenter -t "$UPSTREAM_NS" -n "$W/dnss" -enable_https_to_dns \
		-dns_upstream "10.0.2.2:53" \
		-https_key $W/certs/privkey.pem \
		-https_cert $W/certs/fullchain.pem \
		-https_server_addr "10.0.2.2:443" \
		>> $W/upstream-dnss.log 2>&1 &
	UPSTREAM_DNSS_PID=$!
}

# Start the dnss we are testing, and the prober.
function start_client() {
	local dir=$1

	local url="https://upstream.test/dns-query"
	if [ "$UPSTREAM" == "ip" ]; then
		url="https://10.0.2.2/dns-query"
	fi

	# shellcheck disable=SC2086
	env $DNSS_ENV "$DNSS_BIN" -v 3 \
		-enable_dns_to_https \
		-dns_listen_addr "127.0.0.1:1053" \
		-monitoring_listen_addr "127.0.0.1:1900" \
		-fallback_upstream "10.0.2.2:53" \
		-https_client_cafile $W/certs/fullchain.pem \
		-https_upstream "$url" \
		$DNSS_FLAGS \
		> "$dir/dnss.log" 2>&1 &
	CLIENT_PID=$!

	$W/dnsprobe -server "127.0.0.1:1053" -interval "$PROBE_INTERVAL" \
		> "$dir/probe.log" 2>&1 &
	PROBE_PID=$!
}

function stop_client() {
	kill $PROBE_PID $CLIENT_PID
	wait $PROBE_PID $CLIENT_PID 2> /dev/null || true
}

# Number of goroutines in the dnss being tested.
function goroutines() {
	curl -s "http://127.0.0.1:1900/debug/pprof/goroutine?debug=1" \
		| head -n 1 | grep -o '[0-9]*$' || echo "?"
}

# Number of TCP connections to the upstream DoH server (in any state).
function upstream_conns() {
	ss -Htan dst 10.0.2.2:443 | wc -l
}

# Analyze the probe log, looking at probes sent after the given time (in
# unix ms), and before the cutoff (so we don't use incomplete results).
# Prints how long it took (in seconds after the given time) to get the first
# successful response, and to get $STABLE_COUNT successful responses in a
# row. If they didn't happen, prints "-" instead.
function analyze() {
	local log=$1 from=$2 cutoff=$3
	sort -n -k1,1 "$log" | awk -v from="$from" -v cutoff="$cutoff" \
		-v stable_count=$STABLE_COUNT '
		$1 < from || $1 > cutoff { next }
		{ n++; t[n] = $1; ok[n] = ($3 == "ok") }
		END {
			first = "-"; stable = "-"; run = 0
			for (i = 1; i <= n; i++) {
				if (!ok[i]) { run = 0; continue }
				if (first == "-")
					first = sprintf("%.1f", (t[i] - from) / 1000)
				run++
				if (run == stable_count) {
					stable = sprintf("%.1f",
						(t[i - stable_count + 1] - from) / 1000)
					break
				}
			}
			print first, stable
		}'
}

# Count the failed probes sent in the given time range.
function count_failures() {
	local log=$1 from=$2 to=$3
	awk -v from="$from" -v to="$to" \
		'$1 >= from && $1 < to && $3 != "ok" { n++ } END { print n + 0 }' \
		"$log"
}

# Wait until dnss has recovered (or we give up), after the given time.
function wait_for_recovery() {
	local log=$1 from=$2
	local deadline=$(( from + (RECOVERY_LIMIT + 30) * 1000 ))
	while [ "$(now)" -lt $deadline ]; do
		sleep 1
		# Leave time for in-flight probes to complete.
		local stable
		stable=$(analyze "$log" "$from" $(( $(now) - 2500 )) | cut -d ' ' -f 2)
		if [ "$stable" != "-" ]; then
			return 0
		fi
	done
	return 1
}

#####################################
# Outages. Each one has a function to start it and one to end it.

function linkdown_start() {
	ip link set c0 down
}
function linkdown_end() {
	ip link set c0 up
	ip route replace default via 10.0.1.1
}

function blackhole_start() {
	inr ip route add blackhole 10.0.2.2/32
}
function blackhole_end() {
	inr ip route del blackhole 10.0.2.2/32
}

function ipchange_start() {
	ip addr del $CLIENT_IP/24 dev c0
}
function ipchange_end() {
	if [ "$CLIENT_IP" == "10.0.1.2" ]; then
		CLIENT_IP=10.0.1.3
	else
		CLIENT_IP=10.0.1.2
	fi
	ip addr add $CLIENT_IP/24 dev c0
	ip route replace default via 10.0.1.1
}

function upstream-restart_start() {
	kill $UPSTREAM_DNSS_PID
	wait $UPSTREAM_DNSS_PID 2> /dev/null || true
}
function upstream-restart_end() {
	start_upstream_dnss
}

# start-offline is like linkdown, but the outage starts before dnss does.
function start-offline_start() {
	linkdown_start
}
function start-offline_end() {
	linkdown_end
}

# powercut starts like start-offline, and half way through the link comes up
# but the upstream is blackholed.
function powercut_start() {
	linkdown_start
}
function powercut_middle() {
	linkdown_end
	blackhole_start
}
function powercut_end() {
	blackhole_end
}

#####################################
# Run a single scenario, with the given outage duration.

RESULTS=$W/results.txt

function run() {
	local scenario=$1 duration=$2
	local dir=$W/runs/$scenario-$duration
	mkdir -p "$dir"

	log "## $scenario, ${duration}s outage"

	local pre_failures="-" gr_before="-"
	local outage_start
	if [ "$scenario" == "start-offline" ] || [ "$scenario" == "powercut" ]
	then
		outage_start=$(now)
		"${scenario}_start"
		start_client "$dir"
	else
		start_client "$dir"

		# Warm up, so there is an established connection to the upstream
		# before the outage starts.
		local warmup_start
		warmup_start=$(now)
		sleep 5
		gr_before=$(goroutines)
		outage_start=$(now)
		pre_failures=$(count_failures "$dir/probe.log" \
			$(( warmup_start + 1000 )) "$outage_start")

		log "outage start (conns: $(upstream_conns))"
		"${scenario}_start"
	fi

	# Some scenarios have a middle step, half way through the outage.
	if declare -F "${scenario}_middle" > /dev/null; then
		sleep $(( duration / 2 ))
		"${scenario}_middle"
		log "outage middle step"
		sleep $(( duration - duration / 2 ))
	else
		sleep "$duration"
	fi

	local outage_end
	outage_end=$(now)
	"${scenario}_end"
	log "outage end (conns: $(upstream_conns))"

	local status="PASS"
	if ! wait_for_recovery "$dir/probe.log" "$outage_end"; then
		status="FAIL"
	fi

	# Leave some time for in-flight queries and connections to finish, so
	# the goroutine and connection counts are more meaningful.
	sleep 5
	local gr_after conns
	gr_after=$(goroutines)
	conns=$(upstream_conns)

	local first stable outage_failures
	read -r first stable < <(analyze "$dir/probe.log" "$outage_end" \
		$(( $(now) - 2500 )))
	outage_failures=$(count_failures "$dir/probe.log" \
		"$outage_start" "$outage_end")

	if [ "$stable" == "-" ] || \
		[ "$(echo "$stable > $RECOVERY_LIMIT" | bc)" == "1" ]
	then
		status="FAIL"
	fi

	stop_client

	{
		echo "$scenario $duration $status $first $stable" \
			"$pre_failures $outage_failures $gr_before $gr_after $conns"
	} >> $RESULTS
	log "$status: first ok after ${first}s, stable after ${stable}s"
}

#####################################
# Main.

log "## Setting up (testing $DNSS_BIN)"
setup_network
start_minidns
start_upstream_dnss
sleep 0.5

for scenario in $SCENARIOS; do
	for duration in $DURATIONS; do
		run "$scenario" "$duration"
	done
done

echo
echo "## Results (testing $DNSS_BIN)"
echo "# first/stable: seconds after the outage ended until the first"
echo "#   successful query, and until $STABLE_COUNT successful in a row."
echo "# fail pre/during: failed queries before and during the outage."
echo "# goroutines/conns: before the outage, and at the end."
echo
{
	echo "SCENARIO DURATION RESULT FIRST STABLE FAIL_PRE FAIL_DURING" \
		"GOROUTINES_PRE GOROUTINES_POST CONNS_POST"
	cat $RESULTS
} | column -t

if grep -q FAIL $RESULTS; then
	exit 1
fi
