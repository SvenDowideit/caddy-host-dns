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
#   E2E_MODULE_PATH  module package path   (default github.com/SvenDowideit/caddy-host-dns)
#   E2E_PROVIDER     provider module path (default github.com/caddy-dns/rfc2136)
#   GOOS / GOARCH    target platform      (default linux / host arch)
#   E2E_CADDY_VERSION Caddy version       (optional)
#
# The build service identifies modules by their Go *module* path (the directory
# containing go.mod/go.sum). This repo is a single module whose Caddy app lives
# in the dnsrec subpackage -- the same shape as github.com/mholt/caddy-l4 and
# github.com/ubiuser/caddy-geo-ops, both registered at their module root. So
# register/pass the root path, not the dnsrec subpackage.
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

# Prefer the module root. Also try the dnsrec subpackage, in case it is ever
# split into its own module (with its own go.mod) and registered separately.
candidates=("$module_path")
if [ "$module_path" = "github.com/SvenDowideit/caddy-host-dns" ]; then
	candidates+=("github.com/SvenDowideit/caddy-host-dns/dnsrec")
fi

try_download() {
	local pkg=$1
	local url="https://caddyserver.com/api/download?os=${goos}&arch=${goarch}"
	url="${url}&p=${pkg}"
	url="${url}&p=${provider}"
	[ -n "$caddy_version" ] && url="${url}&caddy=${caddy_version}"
	echo "  $url"
	if curl -fsSL "$url" -o "$output" 2>/dev/null && head -c 4 "$output" | grep -q $'\x7fELF'; then
		return 0
	fi
	rm -f "$output"
	return 1
}

echo "downloading $output from caddyserver.com/download ($goos/$goarch)"
for pkg in "${candidates[@]}"; do
	echo "trying package $pkg"
	if try_download "$pkg"; then
		break
	fi
done

if [ ! -x "$output" ]; then
	echo
	echo "Download failed. The caddyserver.com build service only builds module"
	echo "package paths listed in its registry (https://caddyserver.com/api/packages)."
	echo "Register this module at https://caddyserver.com/account/register-package"
	echo "using the package path above, and ensure it has a version tag; otherwise"
	echo "use 'make e2e' (xcaddy), which has no such restriction."
	exit 1
fi

chmod +x "$output"
echo
"$output" version
"$output" list-modules 2>/dev/null | grep -E 'dns_records|dns\.providers\.' || true

