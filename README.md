# github.com/SvenDowideit/caddy-host-dns

Static, provider-agnostic DNS record management for Caddy.

`dns_records` is a Caddy global option (app module) that declares A, AAAA, and CNAME
records for one or more `dns.providers.*` modules compiled into the same Caddy binary.
It reconciles once at config load and again on every `caddy reload`: records are created
or updated to match the config, and records can be removed with an explicit `remove`
directive. There is no polling, no public-IP discovery, and no per-provider code.

See [caddy-dns-records-module.md](./caddy-dns-records-module.md) for the full design.

## Try it in 30 seconds

A `Makefile` wraps the common workflows. Requires Go and Docker:

```
make help        # list all targets
make test        # unit + Caddyfile parse tests (no network, no credentials)
make conformance # provider conformance smoke test (in-memory, no credentials)
make run         # build Caddy + module, start local PowerDNS, apply examples/Caddyfile
make pdns-down   # stop the local PowerDNS and clean up
```

`make run` builds a Caddy containing this module and the PowerDNS provider, starts a
throwaway PowerDNS via `docker compose`, applies [`examples/Caddyfile`](./examples/Caddyfile),
and serves on `:8080`. Inspect the created records in the local PowerDNS API or with
`make pdns-logs`.

To prove a provider works end-to-end, the suite can be run against a local server
(see [Provider conformance tests](#provider-conformance-tests)):

```
make conformance-bind    # local BIND over RFC2136 — passes the full suite
make conformance-live    # local PowerDNS — see the note on TXT
make bind-down pdns-down # clean up
```

For a full black-box smoke test — Caddy serving HTTP from records it creates,
modifies, and deletes in a containerized BIND — see [`e2e/`](./e2e/):

```
make e2e                 # build Caddy (xcaddy) and run the containerized e2e
make e2e-download        # same, using a caddyserver.com/download binary
```

## Install

Build a Caddy binary that includes this module and the DNS provider(s) you use:

```
xcaddy build \
  --with github.com/SvenDowideit/caddy-host-dns \
  --with github.com/caddy-dns/powerdns
```

Any module registered in the `dns.providers` namespace works; the app never names a
provider in code.

## Use

```
{
	dns_records {
		provider gandi {env.GANDI_TOKEN} {
			zone fi.gy
			record otel.fi.gy    A     10.10.0.5
			record otlp.fi.gy    A     10.10.0.6
			record gateway.fi.gy CNAME otel.fi.gy.
			record obs.fi.gy     A     10.10.0.7 ttl 5m
			remove stale.fi.gy   A
		}
		ttl 1h
	}
}
```

`zone` is optional: it is derived from each record's name when the provider
implements `libdns.ZoneLister`, so set it (top-level as a default, or inside a
provider block) when your provider does not support listing zones.

Equivalent JSON:

```jsonc
{
	"apps": {
		"dns_records": {
			"providers": [
				{
					"dns_provider": { "name": "gandi", "token": "{env.GANDI_TOKEN}" },
					"zone": "fi.gy",
					"records": [
						{ "name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5"] },
						{ "name": "gateway.fi.gy", "type": "CNAME", "value": ["otel.fi.gy."] }
					],
					"remove": [
						{ "name": "stale.fi.gy", "type": "A" }
					]
				}
			],
			"ttl": "1h"
		}
	}
}
```

### Semantics

- A declared `(name, type)` RRset is owned: each reconcile replaces it with exactly the
  declared values, pruning extras. Nothing else in the zone is touched.
- Removing a record from config does not delete it; use the `remove` directive to delete
  an entire `(name, type)` RRset or specific values on every reconcile. Removals are
  resolved against the zone's live records first, so they are TTL-independent.
- `zone` is derived from the record name when the provider implements `libdns.ZoneLister`;
  otherwise set it explicitly (top-level or per provider).
- `best_effort` downgrades reconcile failures to logged errors instead of failing the
  config load/reload.

## Provider conformance tests

The `conformance` package runs the official
[`libdns` test suite](https://github.com/libdns/libdns/tree/master/libdnstest) against a
provider you compile into the test binary, proving the provider + its API settings + a
zone all work together. The provider must be a dependency of the test binary; the
bundled examples register PowerDNS and RFC2136, and you can add your own.

There are two ways to run it:

- **Against a provider's real API**, by supplying its `dns_provider` JSON and a zone:

  ```sh
  CONFORMANCE_PROVIDER_JSON='{"name":"cloudflare","api_token":"..."}' \
  CONFORMANCE_ZONE=example.com. \
  go test -tags conformance -v ./conformance/...
  ```

- **Against a local server**, using the provided `docker compose` services:

  ```sh
  make conformance-bind   # local BIND, RFC2136 dynamic updates — passes fully
  make conformance-live   # local PowerDNS — fails the TXT cases (see below)
  ```

> **Use a dedicated test zone.** The suite creates, modifies, and deletes records named
> `test-*`. Never point it at a zone with real data.

### A note on PowerDNS and TXT

`make conformance-live` currently **fails the suite's TXT test cases**, because of a known
bug in the upstream [`libdns/powerdns`](https://github.com/libdns/powerdns) provider: its
read path (`GetRecords`) never unquotes TXT values, so they come back wrapped in extra
quotes (e.g. `"\"hello\""` instead of `"hello"`). libdns's `TXT.RR()` requires the `Data`
to equal the unquoted `Text`, so the round-trip comparison fails. This is tracked upstream
(see `libdns/powerdns` PR #12).

Everything else passes: the A, AAAA, and CNAME cases are green, and those are the only
record types `dns_records` manages. This is exactly what a conformance suite is for — it
tells you *where* the problem is. For a fully green local run, use
`make conformance-bind`, which exercises the same wrapper against
[`libdns/rfc2136`](https://github.com/libdns/rfc2136) and a local BIND.

### Adding your own provider

Add a test file with the provider imported and call the wrapper:

```go
//go:build conformance

package conformance_test

import (
	"encoding/json"
	"testing"

	"github.com/SvenDowideit/caddy-host-dns/conformance"
	_ "github.com/caddy-dns/cloudflare"
)

func TestMyProvider(t *testing.T) {
	provider := json.RawMessage(`{"name":"cloudflare","api_token":"..."}`)
	conformance.Run(t, provider, "example.com.")
}
```

See [`conformance/example/providers_test.go`](./conformance/example/providers_test.go) for
the PowerDNS and RFC2136 examples.

## License

Apache-2.0. See [LICENSE](./LICENSE).
