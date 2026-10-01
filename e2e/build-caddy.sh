#!/usr/bin/env bash
#
# Build a Caddy binary with the dns_records module (and a dns.providers.*
# module) inside a container, reusing the host's Go module and build caches via
# bind mounts so nothing is re-downloaded or re-compiled unnecessarily.
#
# Using `docker run` with -v (rather than `docker build`) is deliberate: this
# Docker has no BuildKit/buildx, so `RUN --mount=type=cache` is unavailable and
# a Dockerfile build would freeze gigabytes of Go caches into image layers.
#
# Output: e2e/bin/caddy (override with E2E_CADDY_BIN).
#
# Environment:
#   E2E_MODULE_PATH   module under test     (default github.com/SvenDowideit/caddy-host-dns)
#   E2E_PROVIDER      provider module path  (default github.com/caddy-dns/rfc2136)
#   E2E_XCADDY_VERSION xcaddy version        (default v0.4.7)
#   E2E_CADDY_VERSION  Caddy version         (default: xcaddy's)
#   E2E_BUILDER_IMAGE  builder image        (default golang:1.26-alpine)
#   GOOS / GOARCH      target platform       (default linux / host arch)
#   GOMODCACHE / GOCACHE  host cache dirs    (default: `go env`)
#   E2E_INSECURE_SUMDB  set to 1 to set GOSUMDB=off (offline/air-gapped)
#
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

module_path="${E2E_MODULE_PATH:-github.com/SvenDowideit/caddy-host-dns}"
provider="${E2E_PROVIDER:-github.com/caddy-dns/rfc2136}"
xcaddy_version="${E2E_XCADDY_VERSION:-v0.4.7}"
caddy_version="${E2E_CADDY_VERSION:-}"
builder_image="${E2E_BUILDER_IMAGE:-golang:1.26-alpine}"

output="${E2E_CADDY_BIN:-e2e/bin/caddy}"
goos="${GOOS:-linux}"
goarch="${GOARCH:-$(go env GOARCH)}"

gomodcache="${GOMODCACHE:-$(go env GOMODCACHE)}"
gocache="${GOCACHE:-$(go env GOCACHE)}"
# Mount GOPATH/pkg (not just GOMODCACHE) so the module cache *and* the checksum
# database cache ($GOPATH/pkg/sumdb) are available, avoiding re-downloads and
# sumdb verification failures offline.
gopath_pkg="$(go env GOPATH)/pkg"

mkdir -p "$(dirname "$output")"

# GOTOOLCHAIN=local keeps the builder from downloading a different toolchain.
# CGO_ENABLED=0 produces a static binary runnable on the alpine runtime image.
build_env="GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 GOTOOLCHAIN=local"
[ "${E2E_INSECURE_SUMDB:-0}" = "1" ] && build_env="$build_env GOSUMDB=off"

with_args="--with $module_path=/src --with $provider"
[ -n "$caddy_version" ] && with_args="$with_args --caddy-version $caddy_version"

echo "building $output with $builder_image ($goos/$goarch)"
echo "  go mod cache: $gopath_pkg/mod"
echo "  build  cache: $gocache"

docker run --rm \
	--user "$(id -u):$(id -g)" \
	-e HOME=/tmp \
	-e GOCACHE=/tmp/gocache \
	-v "$gopath_pkg":/go/pkg \
	-v "$gocache":/tmp/gocache \
	-v "$repo_root":/src \
	-w /src \
	"$builder_image" \
	sh -c "$build_env go run github.com/caddyserver/xcaddy/cmd/xcaddy@$xcaddy_version build \
		--output /src/$output $with_args"

echo
"$output" version
"$output" list-modules 2>/dev/null | grep -E 'dns_records|dns\.providers\.' || true
