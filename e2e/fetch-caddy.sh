#!/usr/bin/env bash
#
# Download a Caddy binary with the dns_records module (and a dns.providers.*
# module) from https://caddyserver.com/download, instead of building locally.
#
# The build service only builds package paths listed in its registry
# (https://caddyserver.com/api/packages). Once the module is registered (and a
# version tag exists), this works with no Go toolchain on your machine.
#
# Output: e2e/bin/caddy (override with E2E_CADDY_BIN).
#
# Environment:
#   E2E_MODULE_PATH  module under test    (default github.com/SvenDowideit/caddy-host-dns)
#   E2E_PROVIDER     provider module path (default github.com/caddy-dns/rfc2136)
#   GOOS / GOARCH    target platform      (default linux / host arch)
#   E2E_CADDY_VERSION Caddy version       (optional)
#
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

module_path="${E2E_MODULE_PATH:-github.com/SvenDowideit/caddy-host-dns}"
provider="${E2E_PROVIDER:-github.com/caddy-dns/rfc2136}"
caddy_version="${E2E_CADDY_VERSION:-}"

output="${E2E_CADDY_BIN:-e2e/bin/caddy}"
goos="${GOOS:-linux}"
goarch="${GOARCH:-$(go env GOARCH 2>/dev/null || uname -m)}"

mkdir -p "$(dirname "$output")"

url="https://caddyserver.com/api/download?os=${goos}&arch=${goarch}"
url="${url}&p=${module_path}"
url="${url}&p=${provider}"
[ -n "$caddy_version" ] && url="${url}&caddy=${caddy_version}"

echo "downloading $output from caddyserver.com/download ($goos/$goarch)"
echo "  $url"

if ! curl -fsSL "$url" -o "$output"; then
	echo
	echo "Download failed. The caddyserver.com build service only builds module"
	echo "paths listed in its registry (https://caddyserver.com/api/packages)."
	echo "Register the module at https://caddyserver.com/account/register-package,"
	echo "and ensure it has a version tag; otherwise use 'make e2e' (xcaddy)."
	rm -f "$output"
	exit 1
fi

# The service returns JSON on error with HTTP 200 in some cases; guard for it.
if ! head -c 4 "$output" | grep -q $'\x7fELF'; then
	echo
	echo "Download did not return a binary (the service may have returned an error):"
	head -c 400 "$output" || true
	echo
	rm -f "$output"
	exit 1
fi

chmod +x "$output"
echo
"$output" version
"$output" list-modules 2>/dev/null | grep -E 'dns_records|dns\.providers\.' || true
