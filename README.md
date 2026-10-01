# github.com/SvenDowideit/caddy-host-dns

Static, provider-agnostic DNS record management for Caddy.

`dns_records` is a Caddy global option (app module) that declares A, AAAA, and CNAME
records for one or more `dns.providers.*` modules compiled into the same Caddy binary.
It reconciles once at config load and again on every `caddy reload`: records are created
or updated to match the config, and records can be removed with an explicit `remove`
directive. There is no polling, no public-IP discovery, and no per-provider code.

See [caddy-dns-records-module.md](./caddy-dns-records-module.md) for the full design.

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
provider you compile into the test binary, proving the provider + credentials + zone work.

```
CONFORMANCE_PROVIDER_JSON='{"name":"powerdns","server_url":"http://127.0.0.1:8081","api_token":"secret"}' \
CONFORMANCE_ZONE=example.com. \
go test -tags conformance -v ./conformance/...
```

**Use a dedicated test zone**: the suite creates and deletes records named `test-*`.

A local PowerDNS for development is provided via `docker-compose.yml`:

```
docker compose up -d
```

## License

Apache-2.0. See [LICENSE](./LICENSE).
