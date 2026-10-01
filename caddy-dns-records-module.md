# Spec: `caddy_dns_records` — a static, provider-agnostic DNS record Caddy module

Status: Draft (2026-10-01) — awaiting review.
Author: Sven
Related: `docs/otel-home-network-plan.md` §7.1/§7.1a, `docs/adr/0001-dns-tls-mechanism-caddy-first.md`,
`docs/adr/0005-go-cli-shim-pattern.md`.

## 1. Motivation

The home-network OTel plan needs global DNS names for a heterogeneous fleet,
under a dedicated sub-zone (`otel.fi.gy`), and it needs Caddy to hold a valid
certificate for those names **before** it can serve or issue anything for them.

Today `@svendowideit/caddy` covers TLS via ACME **DNS-01**, using a
`github.com/caddy-dns/<provider>` module compiled into the binary. That path
only ever writes **TXT** challenge records, ephemeral and self-managed by ACME.

`github.com/mholt/caddy-dynamicdns` does write **A/AAAA**, but it is a *dynamic
DNS* app: it discovers the machine's IP from an IP-source (public HTTP / UPnP /
interface) and reconciles whatever it finds. There is no way to say "the record
for `x1yoga.otel.fi.gy` is **this fixed address**". For a home fleet with DHCP
reservations and fixed internal addresses, that is the wrong control model.

### 1.1 What we want

A **static** DNS record app for Caddy:

- one or more records, each explicitly configured with its name, type, and
  **literal value(s)**;
- supported types: **`A`**, **`AAAA`**, and **`CNAME`**;
- the provider is **any `dns.providers.*` module already compiled into the
  Caddy binary** — no per-provider code, no hard-coding, no new credential
  handling;
- records are reconciled idempotently on config load (desired → actual), so a
  Caddy restart or `caddy reload` makes DNS match the declared config;
- one credential location (the same provider module the TLS app already uses).

### 1.2 Relationship to existing work

This is deliberately a **simplified, non-dynamic reimplementation of
`caddy-dynamicdns`**: same provider-module integration and record-reconciliation
idea, but the IP/value is **configured**, not discovered, and there is no
timer, no IP sources, and no dynamic-domains scanning.

It is **not** a fork of `caddy-dynamicdns` and does **not** use
`dynamic_dns.ip_sources`. It is a new, small Caddy app module, contributed either
upstream (to Caddy) or to our own repo, and compiled into the binary via the
existing `@svendowideit/caddy` `plugins` mechanism.

## 2. Non-goals

- No public-IP discovery, UPnP, HTTP IP lookups, or interface scanning.
- No background polling / `check_interval`. Reconciliation happens on
  `Provision`/`Start` and on `caddy reload`.
- No dynamic-domains scanning of the HTTP app's route hosts.
- No SRV / TXT / CAA / MX / NS support in v1 (only `A`, `AAAA`, `CNAME`).
- No provider-specific credential schema. That is the provider module's job.
- Not a replacement for ACME DNS-01; the two coexist (TXT vs A/AAAA/CNAME).

## 3. Prior art (verified)

- **`dns.providers.*` namespace** — `modules/caddytls/acmeissuer.go` (the
  `dns` Caddyfile directive) and `caddy-dynamicdns` both load providers with
  `ctx.LoadModule(x, "<field>")` against `namespace=dns.providers inline_key=name`.
  Providers such as `caddy-dns/gandi` register as `dns.providers.gandi` and
  satisfy a `libdns` interface.
- **`libdns` interfaces** (`github.com/libdns/libdns`):
  `RecordGetter` (`GetRecords`), `RecordAppender` (`AppendRecords`),
  `RecordSetter` (`SetRecords`), `RecordDeleter` (`DeleteRecords`),
  `ZoneLister` (`ListZones`).
- **`libdns` record types**: `Address{Name, TTL, IP netip.Addr}` (A *or* AAAA,
  chosen by `IP.Is6()`), `CNAME{Name, TTL, Target}`; each has `RR() RR`
  (`RR{Name, TTL, Type, Data}`).
- **Reconciliation semantics**: `libdns.RecordSetter.SetRecords(ctx, zone, recs)`
  guarantees that for every `(name, type)` pair in the input, *only* those
  records exist in the output — it appends, modifies, **or deletes** to match.
  Providers implement append + delete in terms of it.
- **`Record` name/zone conventions**: record names are relative to a zone
  (`libdns.RelativeName` / `AbsoluteName`; `@` = zone apex).

## 4. Design

### 4.1 Module identity

| Field | Value |
| --- | --- |
| JSON app name | `dns_records` |
| Go module ID | `dns_records` |
| Caddyfile global option | `dns_records` |
| Package | e.g. `github.com/<owner>/caddy-dnsrec` (or upstreamed) |

Register in `init()` with `caddy.RegisterModule(App{})` and
`httpcaddyfile.RegisterGlobalOption("dns_records", parseApp)`, exactly as
`caddy-dynamicdns` does for `dynamic_dns`.

### 4.2 Config schema

Caddyfile (global option):

```
{
	dns_records {
		provider gandi {env.GANDI_BEARER_TOKEN}

		record otel.fi.gy         A     10.10.0.5
		record otlp.fi.gy         A     10.10.0.6
		record gateway.fi.gy      CNAME otel.fi.gy.

		# per-record TTL override (else global / provider default)
		record obs.otel.fi.gy     A     10.10.0.7 ttl 5m

		ttl 1h
	}
}
```

Equivalent JSON:

```jsonc
{
  "apps": {
    "dns_records": {
      "dns_provider": {
        "name": "gandi",
        "bearer_token": "{env.GANDI_BEARER_TOKEN}"
      },
      "records": [
        { "name": "otel.fi.gy",  "type": "A",     "zone": "fi.gy", "value": ["10.10.0.5"] },
        { "name": "otlp.fi.gy",  "type": "A",     "zone": "fi.gy", "value": ["10.10.0.6"] },
        { "name": "gateway.fi.gy","type": "CNAME", "zone": "fi.gy", "value": ["otel.fi.gy."] }
      ],
      "ttl": "1h"
    }
  }
}
```

### 4.3 Go types

```go
type App struct {
    // Any module registered in namespace "dns.providers".
    DNSProviderRaw json.RawMessage `json:"dns_provider,omitempty" caddy:"namespace=dns.providers inline_key=name"`

    // Static records to reconcile.
    Records []RecordSpec `json:"records,omitempty"`

    // Default TTL applied when a record omits its own. Optional.
    TTL caddy.Duration `json:"ttl,omitempty"`

    dnsProvider libdns.RecordSetter
    logger      *zap.Logger
}

type RecordSpec struct {
    // FQDN of the record (fully qualified; not relative to the zone).
    Name  string `json:"name,omitempty"`

    // "A", "AAAA", or "CNAME".
    Type  string `json:"type,omitempty"`

    // Zone the record belongs to (e.g. "fi.gy"). Optional; if empty, derived
    // from Name by longest-suffix match against libdns.ZoneLister, else an error.
    Zone  string `json:"zone,omitempty"`

    // One or more values: IP literals for A/AAAA, a single hostname for CNAME.
    Value []string `json:"value,omitempty"`

    // Optional per-record TTL override.
    TTL caddy.Duration `json:"ttl,omitempty"`
}
```

### 4.4 Lifecycle

1. **`Provision(ctx)`**
   - `ctx.LoadModule(a, "DNSProviderRaw")` → assert `libdns.RecordSetter`.
     If the configured provider cannot set records, fail with a clear error.
   - Validate every `RecordSpec`:
     - type ∈ {`A`,`AAAA`,`CNAME`};
     - `A`/`AAAA`: every value parses via `netip.ParseAddr` and **matches the
       record type** (`A`⇒IPv4, `AAAA`⇒IPv6) — reject mismatches rather than
       silently emitting the wrong RR type;
     - `CNAME`: exactly one value, a hostname (not an IP);
     - `Name` and `Zone` both non-empty after resolution.
   - Resolve `Zone` if omitted: if the provider implements `libdns.ZoneLister`,
     pick the longest zone from `ListZones()` that is a suffix of `Name`;
     otherwise require the operator to set `Zone` explicitly.
   - Apply TTL: record `TTL` → app `TTL` → `0` (provider default).

2. **`Start()`** — one immediate reconcile pass, then return. No goroutine, no
   ticker. (`caddy-dynamicdns` polls; we deliberately do not.)

3. **`Stop()`** — no-op.

Reconcile pass, grouped by `(zone, type)`:

- Build `[]libdns.Record`:
  - `A`/`AAAA` → one `libdns.Address` per value (`Name` relative to zone,
    `TTL`, `IP`);
  - `CNAME` → one `libdns.CNAME` (`Name`, `TTL`, `Target`).
- Call `dnsProvider.SetRecords(ctx, zone, recs)` once per zone.
  `SetRecords` is the desired-state operation: it creates missing records,
  updates changed ones, and removes stale ones in that `(name, type)` RRset.
- Log a compact summary. On error, log and (see §4.6) fail unless
  `best_effort` is set.

### 4.5 Idempotency

Because `SetRecords` is defined as "for each `(name, type)` in the input, these
are the only members of the RRset after the call", re-running on every reload
converges and prunes drift. This satisfies the plan's "declarative and
idempotent; drift is reported, not feared" principle. A dry-run/plan mode may
be added later (see §7).

### 4.6 Error handling

- Config/validation errors fail `Provision` (Caddy refuses the config).
- Runtime API errors during `Start`: by default **fail reload** so a typo can
  never silently leave DNS wrong. An optional `best_effort` flag downgrades a
  reconcile failure to a logged error (useful if the provider API is flaky at
  boot and a later reload will retry).

## 5. Provider-agnosticism (explicit)

- The app **never** names a provider, a credential field, or an env var in code.
- `DNSProviderRaw` is a Caddy module reference; the operator's `provider`
  directive (`provider gandi {env.GANDI_BEARER_TOKEN}`) is unmarshalled by
  whatever `dns.providers.*` module is compiled in. If `github.com/caddy-dns/gandi`
  is not compiled in, `ctx.LoadModule` fails with Caddy's normal
  "module not registered" error — no special-casing.
- Credentials live in the provider's own module config, placed there by the
  caller. For our deployment that means the same `EnvironmentFile` +
  `{env.*}` placeholder path `@svendowideit/caddy` already uses for DNS-01, so
  there is exactly **one** credential location for a given provider.

## 6. Integration with `@svendowideit/caddy`

- Add a new global arg, e.g. `dnsRecords` (array of {name,type,value,zone,ttl}),
  rendered into the JSON config as `apps.dns_records.records`, reusing the
  existing `configureTls` provider/`providerConfig` machinery to emit
  `apps.dns_records.dns_provider` (same env-var placeholders, same
  `EnvironmentFile`).
- Merge semantics: like routes, `dnsRecords` from multiple models merge on
  `(name, type)`; a conflicting duplicate `(name,type)` with different values is
  an error naming both models (mirrors `mergeDesired` for routes/listenAddrs).
- Surface DNS record state in `audit`/`plan`: desired records (per model, then
  merged), and — where the provider supports `RecordGetter` — the live records
  for the zone, with `onlyDesired` / `onlyActual` / `inSync`, so `audit` keeps
  being the one command that says what is going on.
- The `dns_records` module must be added to the `plugins` list the model
  compiles into the binary (alongside the `caddy-dns/*` provider). If we
  upstream it, users add it as a normal plugin; if it stays in our repo, the
  model pins our module path.

## 7. Testing

- **Unit (Go)** with a fake `libdns.RecordSetter` / `ZoneLister`:
  - A/AAAA/CNAME → correct `libdns` record construction (relative names, types
    chosen by IP family, TTL precedence);
  - validation rejects: unknown type, IP/type mismatch, CNAME with 0 or >1
    values, CNAME target that is an IP, un-resolvable zone;
  - reconcile calls `SetRecords` once per zone with the expected records;
  - `best_effort` on/off changes whether a provider error fails `Start`.
- **Caddyfile parse test** mirroring `caddyfile_test.go` in
  `caddy-dynamicdns` (parse → JSON shape).
- **Integration (opt-in, real provider)**: set one record in a scratch zone,
  assert via `RecordGetter` (or `dig`), then set a changed value and assert the
  old value is gone (proves `SetRecords` pruning).
- **Live smoke**: `x1yoga.otel.fi.gy A <fixed IP>` via the existing gandi
  token; `dig` confirms; `audit` reports `inSync`.

## 8. Phasing

- **v1**: global option, single provider, static `A`/`AAAA`/`CNAME`, provision +
  start + `SetRecords`, validation, tests. This is the whole of §4.
- **v1.1**: `best_effort`, per-record TTL, zone auto-resolution via `ZoneLister`.
- **Deferred**: a `verify`/plan action (desired-vs-actual diff via
  `RecordGetter`) so `audit` can show live drift; support for additional record
  types (SRV/TXT/CAA) if the plan later needs them.

## 9. Consequences

- Positive: fixed internal names are expressible and reconciled from declared
  config, with **no new provider code** and no second credential store; the
  same compiled-in `caddy-dns/*` module serves both TXT (ACME) and A/AAAA/CNAME.
- Positive: eliminates the Go-CLI-shim path (§7.1b / ADR-0005) for the common
  A/AAAA/CNAME case; that shim remains only for record ops Caddy still cannot
  express (e.g. SRV), if ever needed.
- Negative: a Caddy app module must be built and shipped/pinned; it must track
  `libdns` API changes. Mitigated by small surface (3 record types) and upstream
  option.

## 10. Open questions

1. **Home**: upstream to Caddy (broadest value, review latency) vs. our own
   module repo now (faster, then upstream later)? This determines the `plugins`
   entry and whether `@svendowideit/caddy` must pin a module path.
2. **Deletion policy**: `SetRecords` prunes *all* records for a `(name,type)` in
   the zone that are not declared. For a managed sub-zone that is correct; if the
   app is ever pointed at a zone with hand-managed records of the same name/type,
   it will delete them. Do we want an `update_only` guard (like
   `caddy-dynamicdns`) that only sets existing records and never creates?
3. **Zone default**: require explicit `Zone` in v1 (simplest, unambiguous) and
   add `ZoneLister` derivation in v1.1? Or derive from the start?
4. **CNAME at apex / coexistence**: `SetRecords` with CNAME will remove other
   non-DNSSEC records at that name (per `libdns` docs). Acceptable for our
   dedicated sub-zone, but should we document a hard warning?
