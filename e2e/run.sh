#!/usr/bin/env bash
#
# End-to-end smoke test for the dns_records Caddy module.
#
# Runs, entirely in Docker containers on a private network:
#   - BIND           : authoritative zone, accepts RFC2136 dynamic updates (TSIG)
#   - Caddy          : binary built with dns_records + a dns.providers.* module
#   - tools          : dig + curl, used for every stage and verification step
#
# Each stage reloads a Caddyfile (via `docker exec ... caddy reload`), then every
# verification step runs through `docker exec` (dig for DNS, curl for HTTP).
#
# Stages:
#   1. create : declare alpha(A x2), beta(A), gamma(A)  -> verify DNS + HTTP
#   2. modify : prune alpha, change beta, add delta(CNAME) -> verify DNS + HTTP
#   3. delete : explicitly remove beta and gamma          -> verify DNS + HTTP
#
# Usage:
#   ./e2e/run.sh                 # build Caddy via xcaddy (container + host caches)
#   E2E_SKIP_BUILD=1 ./e2e/run.sh  # reuse an existing e2e/bin/caddy
#
# Set E2E_CADDY_BIN to use a binary built elsewhere (e.g. by
# `make e2e-download`, which fetches from https://caddyserver.com/download).
#
set -euo pipefail

cd "$(dirname "$0")"

COMPOSE=(docker compose -f docker-compose.yml)
# These match the container_name values in docker-compose.yml.
CONT_CADDY="caddy-e2e"
CONT_BIND="bind-e2e"
CONT_TOOLS="tools-e2e"

CADDY_NET1_IP=172.31.240.10
CADDY_NET2_IP=172.31.241.10
DNS_IP=172.31.240.11
DNS=bind-e2e

PASS=0
FAIL=0

say()  { printf '\n\033[1;34m== %s ==\033[0m\n' "$*"; }
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

# exec_in <container> <cmd...>
exec_in() { local c=$1; shift; docker exec "$c" "$@"; }

# dig_a <name> -> sorted, space-joined A record values
dig_a() {
	exec_in "$CONT_TOOLS" dig +short A "$1" "@$DNS" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//'
}

# dig_cname <name> -> CNAME target (trailing dot stripped)
dig_cname() {
	exec_in "$CONT_TOOLS" dig +short CNAME "$1" "@$DNS" 2>/dev/null | sed 's/\.$//' | tr '\n' ' ' | sed 's/ $//'
}

# http_get <host> <caddy-ip> -> response body, pinning the host to one endpoint
http_get() {
	exec_in "$CONT_TOOLS" curl -fsS --max-time 5 \
		--resolve "$1:80:$2" "http://$1/" 2>/dev/null
}

# check_a <name> <expected-values...>
check_a() {
	local name=$1; shift
	local want="$*" got
	got=$(dig_a "$name")
	if [ "$got" = "$want" ]; then
		ok "$name A = [$got]"
	else
		bad "$name A = [$got] (want [$want])"
	fi
}

# check_no_a <name>  (name must have no A records)
check_no_a() {
	local name=$1 got
	got=$(dig_a "$name")
	if [ -z "$got" ]; then
		ok "$name has no A records"
	else
		bad "$name A = [$got] (want none)"
	fi
}

# check_cname <name> <expected-target>
check_cname() {
	local name=$1 want=$2 got
	got=$(dig_cname "$name")
	if [ "$got" = "$want" ]; then
		ok "$name CNAME = $got"
	else
		bad "$name CNAME = [$got] (want [$want])"
	fi
}

# check_http <label> <host> <caddy-ip> <expected-site>
# Verifies the body contains site=<expected-site> and served-by=<caddy-ip>.
check_http() {
	local label=$1 host=$2 ip=$3 site=$4 body
	body=$(http_get "$host" "$ip") || { bad "$label ($host via $ip) no response"; return; }
	if printf '%s' "$body" | grep -q "site=$site" && printf '%s' "$body" | grep -q "served-by=$ip"; then
		ok "$label: $host via $ip -> $body"
	else
		bad "$label: $host via $ip -> [$body] (want site=$site served-by=$ip)"
	fi
}

# check_http_not_served <label> <host> <caddy-ip>
# Passes if the endpoint does not serve the host's site (connection refused, or
# Caddy's empty default response for an unknown host).
check_http_not_served() {
	local label=$1 host=$2 ip=$3 body
	body=$(http_get "$host" "$ip" 2>/dev/null || true)
	if printf '%s' "$body" | grep -q "^site="; then
		bad "$label: $host via $ip unexpectedly served -> [$body]"
	else
		ok "$label: $host via $ip not served"
	fi
}

# reload <caddyfile>
reload() {
	say "reload Caddy with $1"
	exec_in "$CONT_CADDY" caddy reload --config "/configs/$1" --adapter caddyfile
	# Give the dns_records app a moment to reconcile to BIND.
	sleep 1
}

wait_ready() {
	say "waiting for containers"
	local i
	for i in $(seq 1 60); do
		if exec_in "$CONT_TOOLS" curl -fsS --max-time 2 \
			--resolve "alpha.example.com:80:$CADDY_NET1_IP" http://alpha.example.com/ >/dev/null 2>&1; then
			echo "  caddy is serving"
			return 0
		fi
		sleep 1
	done
	echo "  caddy did not become ready"
	return 1
}

cleanup() {
	local code=$?
	if [ $code -ne 0 ]; then
		say "failure — container logs"
		"${COMPOSE[@]}" logs --no-color --tail=80 || true
	fi
	say "tearing down"
	"${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
	exit $code
}
trap cleanup EXIT

# --- build & start ---------------------------------------------------------

# The Caddy binary is produced by e2e/build-caddy.sh into e2e/bin/caddy, reusing
# the host Go caches, then copied into a small runtime image. Set
# E2E_SKIP_BUILD=1 to reuse an existing e2e/bin/caddy.
if [ "${E2E_SKIP_BUILD:-0}" != "1" ]; then
	say "building Caddy binary (${E2E_SOURCE:-xcaddy})"
	./build-caddy.sh
else
	say "skipping Caddy build (E2E_SKIP_BUILD=1)"
	[ -x bin/caddy ] || { echo "  e2e/bin/caddy missing"; exit 1; }
fi

say "building runtime images and starting containers"
"${COMPOSE[@]}" up -d --build

wait_ready

# --- stage 1: create -------------------------------------------------------

reload 00-create.caddyfile

say "stage 1 DNS: records created"
check_a alpha.example.com "$CADDY_NET1_IP $CADDY_NET2_IP"
check_a beta.example.com  "$CADDY_NET1_IP"
check_a gamma.example.com "$CADDY_NET2_IP"
check_no_a delta.example.com

say "stage 1 HTTP: each name served from each endpoint with distinct HTML"
check_http "alpha/net1" alpha.example.com "$CADDY_NET1_IP" alpha
check_http "alpha/net2" alpha.example.com "$CADDY_NET2_IP" alpha
check_http "beta/net1"  beta.example.com  "$CADDY_NET1_IP" beta
check_http "gamma/net2" gamma.example.com "$CADDY_NET2_IP" gamma

# --- stage 2: modify -------------------------------------------------------

reload 01-modify.caddyfile

say "stage 2 DNS: alpha pruned, beta changed, delta CNAME added"
check_a alpha.example.com "$CADDY_NET2_IP"
check_a beta.example.com  "$CADDY_NET2_IP"
check_cname delta.example.com alpha.example.com

say "stage 2 HTTP: alpha now only on net2; delta CNAME serves its own site"
check_http "alpha/net2" alpha.example.com "$CADDY_NET2_IP" alpha
check_http_not_served "alpha/net1 pruned" alpha.example.com "$CADDY_NET1_IP"
check_http "beta/net2"  beta.example.com  "$CADDY_NET2_IP" beta
check_http "delta/net2" delta.example.com "$CADDY_NET2_IP" delta

# --- stage 3: delete -------------------------------------------------------

reload 02-delete.caddyfile

say "stage 3 DNS: beta and gamma explicitly removed, alpha and delta kept"
check_a alpha.example.com "$CADDY_NET2_IP"
check_cname delta.example.com alpha.example.com
check_no_a beta.example.com
check_no_a gamma.example.com

say "stage 3 HTTP: removed names no longer served"
check_http "alpha/net2 kept" alpha.example.com "$CADDY_NET2_IP" alpha
check_http "delta/net2 kept" delta.example.com "$CADDY_NET2_IP" delta
check_http_not_served "beta removed"  beta.example.com  "$CADDY_NET2_IP"
check_http_not_served "gamma removed" gamma.example.com "$CADDY_NET2_IP"

# --- summary ---------------------------------------------------------------

say "summary"
printf '  %d passed, %d failed\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then
	exit 1
fi
echo "  e2e OK"
