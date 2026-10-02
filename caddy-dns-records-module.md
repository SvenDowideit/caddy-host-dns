# Plan: `dns_records` — a static, provider-agnostic DNS record Caddy module (with a conformance test suite)

Status: Refined plan (2026-10-02) — ready to implement.
Author: Sven
Supersedes: earlier draft of this file.
Related: `docs/otel-home-network-plan.md` §7.1/§7.1a, `docs/adr/0001-dns-tls-mechanism-caddy-first.md`,
`docs/adr/0005-go-cli-shim-pattern.md`.

Repo: `github.com/SvenDowideit/caddy-host-dns` (this repo), Apache-2.0.

---

## 1. Motivation

The home-network OTel plan needs global DNS names for a heterogeneous fleet under a
dedicated sub-zone (`otel.fi.gy`), and Caddy needs a valid certificate for those names
before it can serve them.

Two existing mechanisms fall short:

- **ACME DNS-01** (`github.com/caddy-dns/<provider>`) only writes ephemeral **TXT**
  challenge records, self-managed by ACME. It cannot hold a fixed **A/AAAA/CNAME**.
- **`github.com/mholt/caddy-dynamicdns`** writes A/AAAA, but it is a *dynamic DNS* app:
  it discovers the machine's IP from an IP source (public HTTP / UPnP / interface) and
  reconciles whatever it finds. There is no way to declare "the record for
  `x1yoga.otel.fi.gy` is **this fixed address**". For a fleet with DHCP reservations and
  fixed internal addresses, that is the wrong control model.

### 1.1 What we want

A **static** DNS record app for Caddy:

- records explicitly configured with name, type, and **literal value(s)**;
- supported types: **`A`**, **`AAAA`**, **`CNAME`**;
- the provider is **any `dns.providers.*` module already compiled into the Caddy
  binary** — no per-provider code, no hard-coded credentials;
- records reconciled idempotently on `Provision`/`Start` and on `caddy reload`
  (desired → actual), so config is the source of truth;
- one credential location (the same provider module the TLS app already uses);
- a **provider-agnostic test suite** so any user can prove their provider + API settings
  + domain work, or get a clear report of what did not.

### 1.2 Relationship to `caddy-dynamicdns`

Deliberately a **simplified, non-dynamic reimplementation**: same provider-module
integration, same multi-provider normalization and Caddyfile parsing patterns, same
record-reconciliation idea — but the value is **configured**, not discovered, and there
is no timer, no IP sources, and no dynamic-domains scanning.

We **port** (not depend on) the following proven patterns from `caddy-dynamicdns`:
`normalizeProviders`, `Provider` with per-provider reserved `record`/`remove` blocks,
`splitProviderSegment`, `unmarshalModuleTokens`, and the Caddyfile parse-test approach.

It is **not** a fork and does **not** use `dynamic_dns.ip_sources`. It is a new, small
Caddy app module in this repo, compiled into Caddy via `xcaddy` / the download page.

---

## 2. Non-goals

- No public-IP discovery, UPnP, HTTP IP lookups, or interface scanning.
- No background polling / `check_interval`. Reconciliation happens on
  `Provision`/`Start` and on reload.
- No dynamic-domains scanning of the HTTP app's route hosts.
- No SRV / TXT / CAA / MX / NS support in v1 (only `A`, `AAAA`, `CNAME`).
- No provider-specific credential schema. That is the provider module's job.
- Not a replacement for ACME DNS-01; the two coexist (TXT vs A/AAAA/CNAME).
- No in-Caddy test command. Caddy can't run provider tests against itself; testing is
  external (see §6).

---

## 3. Prior art (verified)

- **`dns.providers.*` namespace** — `dynamicdns.go` loads providers with
  `ctx.LoadModule(&a.Providers[i], "DNSProviderRaw")` against
  `namespace=dns.providers inline_key=name`, and asserts the result to
  `libdns.RecordSetter`.
- **`libdns` interfaces** (`github.com/libdns/libdns`): `RecordGetter` (`GetRecords`),
  `RecordAppender` (`AppendRecords`), `RecordSetter` (`SetRecords`), `RecordDeleter`
  (`DeleteRecords`), `ZoneLister` (`ListZones`). `ZoneLister` is optional.
- **`libdns` record types**: `Address{Name, TTL, IP netip.Addr}` (A *or* AAAA, chosen by
  `IP.Is6()`), `CNAME{Name, TTL, Target}`; each has `RR() RR`.
- **Reconciliation semantics**: `libdns.RecordSetter.SetRecords(ctx, zone, recs)` defines
  desired state — for every `(name, type)` pair in the input, *only* those records exist
  after the call; it appends, modifies, or deletes as needed. Providers implement append
  + delete in terms of it. `SetRecords` may be non-atomic; `libdns.AtomicErr` signals
  atomic failure.
- **Name/zone conventions**: record names are relative to a zone; use `libdns.RelativeName`
  / `libdns.AbsoluteName`; `@` = zone apex; CNAME targets should have a trailing dot.
- **CNAME caution** (libdns docs): using `SetRecords` to add a CNAME where non-DNSSEC
  records exist may fail, violate DNS standards, or remove the other records. Document a
  warning; acceptable for a dedicated managed sub-zone.
- **Official conformance suite** (`github.com/libdns/libdns/libdnstest`): `TestSuite`
  wraps a `Provider` (Getter+Appender+Setter+Deleter, optionally ZoneLister via
  `WrapNoZoneLister`), and `RunTests(t)` exercises `ListZones`, `GetRecords`,
  `AppendRecords`, `SetRecords`, `DeleteRecords`, with automatic `test-*` cleanup,
  `SkipRRTypes`, and `ExpectEmptyZone`. It operates on a **live provider object in the
  test process**, so the provider module must be compiled into the test binary.
- **`xcaddy`** builds Caddy with plugins:
  `xcaddy build --with github.com/... --with github.com/caddy-dns/<provider>`.
- **`caddytest`** (`caddyserver/caddy/caddytest`) runs an in-process Caddy and POSTs
  configs to the admin API. It is used inside Caddy's own repo; it drives a real Caddy
  and cannot reach a provider that lives in a different process/container.

---

## 4. Module design

### 4.1 Identity

| Field | Value |
| --- | --- |
| JSON app name | `dns_records` |
| Go module ID | `dns_records` |
| Caddyfile global option | `dns_records` |
| Go package | `github.com/SvenDowideit/caddy-host-dns/dnsrec` |
| License | Apache-2.0 |

Register in `init()` with `caddy.RegisterModule(App{})` and
`httpcaddyfile.RegisterGlobalOption("dns_records", parseApp)`, exactly as
`caddy-dynamicdns` does for `dynamic_dns`.

### 4.2 Config schema

Multi-provider (preferred). Each provider owns its records; there is no shared record
list because values are explicit.

Caddyfile (global option):

```
{
	dns_records {
		provider gandi {env.GANDI_TOKEN} {
			zone fi.gy
			record otel.fi.gy    A     10.10.0.5
			record otlp.fi.gy    A     10.10.0.6
			record gateway.fi.gy CNAME otel.fi.gy.
			record obs.fi.gy     A     10.10.0.7 ttl 5m
			# explicit removal, applied on every reconcile:
			# remove stale.fi.gy  A                    # all values of stale.fi.gy/A
			# remove stale2.fi.gy A 10.10.0.99        # only that value
		}
		provider cloudflare {env.CF_API_TOKEN} {
			record example.com @  A     203.0.113.10
		}
		ttl 1h
		# optional:
		# best_effort
	}
}
```

Legacy single-provider shorthand (top-level `provider` + `record`, no per-provider
block) is folded into the providers list by `normalizeProviders`:

```
{
	dns_records {
		provider gandi {env.GANDI_TOKEN}
		record otel.fi.gy A 10.10.0.5
		ttl 1h
	}
}
```

Record syntax: `record <name> <type> <value...> [ttl <duration>]`

- `<name>` is the **fully-qualified** record name (e.g. `otel.fi.gy`).
- `<type>` ∈ `A`, `AAAA`, `CNAME`.
- `<value...>` one or more values (A/AAAA: IPs; CNAME: exactly one hostname).
- optional per-record `ttl`; an optional `zone` field exists in JSON only (derived at
  Provision if omitted — see §4.4).

Removal syntax: `remove <name> <type> [value...]`

- With values: delete exactly those `(name,type,value)` records.
- Without values: delete **all** records of that `(name,type)` RRset.
- Applied on every reconcile via `DeleteRecords`; purely declarative (no memory of prior
  config). Removal is the explicit, opt-in counterpart to `record` create/update.
- Removals are **resolved against the zone's live records first** (via `libdns.RecordGetter`),
  then deleted. This makes whole-RRset removal work even for providers whose
  `DeleteRecords` requires an exact value match (e.g. `libdns/powerdns`), and makes removal
  independent of TTL mismatches.

Zone syntax: `zone <zone>`

- Sets the zone for this provider's records/removals lacking their own `zone`. Optional at
  both levels. When omitted, the zone is derived per name from the provider's
  `libdns.ZoneLister`; if the provider has no `ZoneLister`, a zone directive is required.

Inside a provider's block, the names `record`, `remove`, and `zone` are reserved for this
app; everything else belongs to the DNS provider module (whether inline args or block
subdirectives), same as `domains` in `caddy-dynamicdns`.

Equivalent JSON:

```jsonc
{
  "apps": {
    "dns_records": {
      "providers": [
        {
          "dns_provider": { "name": "gandi", "token": "{env.GANDI_TOKEN}" },
          "records": [
            { "name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5"] },
            { "name": "gateway.fi.gy", "type": "CNAME", "value": ["otel.fi.gy."] }
          ]
        }
      ],
      "ttl": "1h"
    }
  }
}
```

### 4.3 Go types

```go
package dnsrec

type App struct {
	// Any module registered in namespace "dns.providers".
	// Legacy single-provider shorthand; folded into Providers by normalizeProviders.
	DNSProviderRaw json.RawMessage `json:"dns_provider,omitempty" caddy:"namespace=dns.providers inline_key=name"`
	Records        []RecordSpec    `json:"records,omitempty"`

	// One entry per provider (or per account of the same provider).
	Providers []Provider `json:"providers,omitempty"`

	// Default TTL applied when a record omits its own. Optional.
	TTL caddy.Duration `json:"ttl,omitempty"`

	// Default zone for records/removals without their own. Optional.
	Zone string `json:"zone,omitempty"`

	// Downgrade reconcile failures to logged errors instead of failing Start. Optional.
	BestEffort bool `json:"best_effort,omitempty"`

	ctx    caddy.Context
	logger *zap.Logger
}

type Provider struct {
	DNSProviderRaw json.RawMessage `json:"dns_provider,omitempty" caddy:"namespace=dns.providers inline_key=name"`
	Records        []RecordSpec    `json:"records,omitempty"`
	// Explicit removals; applied every reconcile.
	Removals []RemoveSpec `json:"remove,omitempty"`
	// Default zone for this provider. Optional.
	Zone string `json:"zone,omitempty"`

	dnsProvider libdns.RecordSetter
	getter      libdns.RecordGetter
	deleter     libdns.RecordDeleter
}

type RecordSpec struct {
	// FQDN of the record (fully qualified; not relative to the zone).
	Name string `json:"name,omitempty"`

	// "A", "AAAA", or "CNAME".
	Type string `json:"type,omitempty"`

	// Zone the record belongs to (e.g. "fi.gy"). Optional; if empty, derived at
	// Provision from Name by longest-suffix match against ZoneLister.
	Zone string `json:"zone,omitempty"`

	// A/AAAA: one or more IPs; CNAME: exactly one hostname.
	Value []string `json:"value,omitempty"`

	// Optional per-record TTL override.
	TTL caddy.Duration `json:"ttl,omitempty"`
}

type RemoveSpec struct {
	Name  string   `json:"name,omitempty"`
	Type  string   `json:"type,omitempty"`
	Zone  string   `json:"zone,omitempty"`
	Value []string `json:"value,omitempty"` // empty => remove whole (name,type) RRset
}
```

### 4.4 Lifecycle

1. **`Provision(ctx)`**
   - `ctx.Logger`, `normalizeProviders`.
   - For each provider: `ctx.LoadModule(&p, "DNSProviderRaw")` → assert
     `libdns.RecordSetter`; clear error if the provider cannot set records.
   - Validate every `RecordSpec`:
     - type ∈ {`A`,`AAAA`,`CNAME`};
     - `A`: every value parses via `netip.ParseAddr` and is IPv4;
     - `AAAA`: every value parses and is IPv6;
     - `CNAME`: exactly one value, a hostname (not an IP);
     - `Name` non-empty;
   - Validate every `RemoveSpec` the same way, except values are optional.
   - Resolve `Zone` per record/removal: explicit `zone` on the record/removal → the
     provider's `zone` → the app's top-level `zone` → else, if the provider implements
     `libdns.ZoneLister`, the longest zone from `ListZones()` that is a suffix of `Name`
     → else error telling the operator to set `zone`.
   - Apply TTL precedence: record `TTL` → app `TTL` → `0` (provider default).

2. **`Start()`** — one immediate reconcile pass, then return. No goroutine, no ticker.
   On reconcile error: log + return error unless `BestEffort`, in which case log only.

3. **`Stop()`** — no-op.

Reconcile pass, grouped by `zone`:

- Build `[]libdns.Record`:
  - `A`/`AAAA` → one `libdns.Address` per value (`Name` made relative to zone via
    `libdns.RelativeName`, `TTL`, `IP`);
  - `CNAME` → one `libdns.CNAME` (`Name`, `TTL`, `Target`).
- If any removals exist, require the provider to implement both `libdns.RecordGetter` and
  `libdns.RecordDeleter`. For each removal, call `GetRecords`, select live records whose
  relative name and type match (and, when a value is given, whose data equals it), and
  delete those concrete records. Empty-value removals therefore delete the whole RRset
  regardless of the provider's exact-match semantics or TTL.
- Call `p.dnsProvider.SetRecords(ctx, zone, recs)` once per zone (creates/updates; owns
  each declared `(name,type)` RRset). Then `DeleteRecords(ctx, zone, resolved)`. Log a
  compact summary.
- On error: log and fail unless `BestEffort`.

### 4.5 Idempotency, ownership & drift

- **Declared `(name,type)` is owned.** `SetRecords` replaces that RRset with exactly the
  declared values, so re-running converges, updates changed values, and prunes extra
  values at a declared name/type. Nothing else in the zone is touched.
- **Undeclared names persist.** Records whose names were removed from config are not
  deleted automatically — `SetRecords` only receives currently-declared names. This is
  the "only remove when explicitly required" rule.
- **Explicit removal** is the `remove` directive; it is re-applied every reconcile, so it
  is declarative and idempotent (`DeleteRecords` silently ignores already-absent records).
- Optional future safety: a whole-zone authoritative mode is deliberately **not** in v1
  (see §10).

### 4.6 Error handling

- Config/validation errors fail `Provision` (Caddy refuses the config).
- Runtime API errors during `Start` fail the load/reload by default, so a typo cannot
  silently leave DNS wrong. `best_effort` downgrades a reconcile failure to a logged
  error.

### 4.7 Provider-agnosticism (explicit)

- The app **never** names a provider, credential field, or env var in code.
- `DNSProviderRaw` is a Caddy module reference; `provider gandi {env.GANDI_TOKEN}` is
  unmarshalled by whatever `dns.providers.gandi` module is compiled in. If absent,
  `ctx.LoadModule` fails with Caddy's normal "module not registered" error.
- Credentials live in the provider's own module config. For our deployment that is the
  same `EnvironmentFile` + `{env.*}` path the image already uses for DNS-01, so there is
  exactly **one** credential location per provider.

---

## 5. Integration with `@svendowideit/caddy` (out of scope)

Deliberately **out of scope** for this plan. Once the module exists and is published, a
follow-up doc will cover: a `dnsRecords` model arg rendering `apps.dns_records.records`,
reuse of `configureTls` provider machinery for `dns_provider`, merge semantics on
`(name,type)`, `audit`/`plan` surfacing of desired vs live records, and pinning the
module in the image's `plugins` list. Recorded here so it is not lost.

---

## 6. Testing

Testing is external to Caddy. Two layers ship now; a third (containerized black-box E2E)
is designed but **deferred**.

### 6.1 Unit + Caddyfile parse (Go, in this repo)

- Fake `libdns.RecordSetter` / `RecordGetter` / `RecordDeleter` / `ZoneLister`:
  - A/AAAA/CNAME → correct `libdns` construction (relative names, type chosen by IP
    family, TTL precedence);
  - validation rejects: unknown type, IP/type mismatch, CNAME with 0 or >1 values,
    CNAME target that is an IP, unresolved zone;
  - reconcile calls `SetRecords` once per zone with the expected records;
  - `remove` resolves against the provider's `GetRecords` output (live records) and builds
    the expected delete records, including empty-value (whole RRset) and explicit-value
    forms, and calls `DeleteRecords`;
  - using `remove` with a provider lacking `RecordDeleter`/`RecordGetter` fails Provision;
  - `best_effort` on/off changes whether a provider error fails `Start`.
- Caddyfile parse tests mirroring `caddyfile_test.go`: parse → JSON shape, including the
  legacy single-provider fold, multiple providers with nested `record`/`remove` blocks,
  and `remove` with/without values; a `dummyProvider` registered as
  `dns.providers.test_dummy` proves reserved tokens never leak into the provider module.

### 6.2 Provider conformance (`libdnstest`)

Purpose: a user proves **their** provider + API settings + domain work, or gets a clear
report of what did not.

- New package `conformance` wrapping `github.com/libdns/libdns/libdnstest`:
  - builds a provider from a JSON fragment (the same object that goes under
    `dns_provider`) via a caller-supplied constructor, or from env vars;
  - `Run(t)` calls `libdnstest.NewTestSuite(provider, zone).RunTests(t)`, with
    `SkipRRTypes` defaulting to everything except A/AAAA/CNAME/TXT (our v1 scope plus the
    mandatory framework types), `ExpectEmptyZone` on, and a 30s timeout.
- Because providers must be compiled in, the user writes a tiny test file in a repo that
  imports our `conformance` package **and** their provider:
  ```go
  //go:build conformance

  package conformance_test

  import (
      "testing"
      "github.com/SvenDowideit/caddy-host-dns/conformance"
      _ "github.com/caddy-dns/cloudflare"
  )

  func TestMyProvider(t *testing.T) {
      provider := json.RawMessage(`{"name":"cloudflare","api_token":"..."}`)
      conformance.Run(t, provider, "example.com.")
  }
  ```
  `RunFromEnv(t)` (using `CONFORMANCE_PROVIDER_JSON` + `CONFORMANCE_ZONE`) is also provided.
- We ship worked examples (`conformance/example/providers_test.go`) for
  `github.com/caddy-dns/powerdns` and `github.com/caddy-dns/rfc2136`, each gated on its own
  env var so plain `go test -tags conformance ./...` runs only the in-memory smoke.
- **Known upstream issue**: the suite's TXT cases fail against `libdns/powerdns` because its
  read path never unquotes TXT (returns `"\"v\""`), and libdns v1 treats TXT as an essential
  (unskippable) type. A/AAAA/CNAME pass, which is all this module manages. `make
  conformance-live` documents this loudly instead of hiding it.
- To have a **fully green** local run, `make conformance-bind` runs the same wrapper against
  `libdns/rfc2136` + a local BIND (`internetsystemsconsortium/bind9`, RFC2136 dynamic
  updates over TSIG on `:5354`); libdns/rfc2136 handles TXT correctly.
- Synthetic/local backend aid: a `docker-compose.yml` with a default PowerDNS service
  (`powerdns/pdns-auth-49`, API on `:8081`, key `secret`) and a `bind` profile
  (`.docker/bind/`) for RFC2136. No orchestration code — driven by the Makefile.
- CI guard: an always-on conformance **smoke** against `libdns/libdns/libdnstest/example`
  (in-memory provider) proves our wrapper runs the suite correctly, without live creds.

### 6.3 CI

- GitHub Actions: `go build ./...`, `go vet ./...`, `go test ./...` (unit + parse),
  `go test -tags conformance ./conformance/...` (in-memory example smoke), `gofmt` check,
  and an `xcaddy build` step adding `dns_records` + `caddy-dns/powerdns` to prove the
  module compiles into a real Caddy.
- Real-provider conformance is never run in CI (needs secrets); documented as a local,
  opt-in command.

### 6.4 Containerized black-box Caddy + BIND E2E (implemented)

Built in `e2e/`. Runs Caddy against a real BIND (RFC2136 dynamic updates) on a
private Docker network, and verifies through Caddy's own admin API and Caddyfile:

- **Build**: `e2e/build-caddy.sh` runs `xcaddy` inside a container but bind-mounts
  the host `$GOPATH/pkg` and `$GOCACHE`, so nothing is re-downloaded and objects are
  reused; the ~50 MB binary is copied into a small alpine runtime image. `docker run`
  + `-v` is used instead of a Dockerfile build because the target Docker lacks
  BuildKit/buildx (`RUN --mount=type=cache` unavailable), where a Dockerfile build
  would freeze GBs of Go caches into image layers. Also drives a
  `caddyserver.com/download` binary path (`make e2e-download`).
- **Containers**: `bind`, `caddy` (two network endpoints), `tools` (`dig` + `curl`).
- **Stages** (each `caddy reload` via `docker exec`, verified with `dig`/`curl`):
  create (multi-value A across both endpoints, plus A and CNAME), modify (prune an
  A value, change a value, add a CNAME), delete (`remove` whole RRsets). Each site
  `bind`s the endpoint(s) its records point to, so per-endpoint HTTP responses prove
  the served addresses match DNS.
- **Known limitation**: the hosted build service only builds module paths in its
  registry (https://caddyserver.com/api/packages). It keys on the Go **module**
  path (the directory with go.mod/go.sum), not the package that calls
  `RegisterModule`. Register/claim `github.com/SvenDowideit/caddy-host-dns`
  (the module root, which blank-imports `dnsrec`), not the `.../dnsrec` package.
  This mirrors `github.com/mholt/caddy-l4` and `github.com/ubiuser/caddy-geo-ops`,
  which register at their module root while their modules live in subpackages.
  Until registered, `make e2e-caddyserver` fails clearly; `make e2e` (xcaddy) has
  no such restriction.

---

## 7. Repository layout

```
caddy-host-dns/
  go.mod                              # github.com/SvenDowideit/caddy-host-dns
  caddyhostdns.go                     # root package; blank-imports dnsrec for xcaddy
  LICENSE                             # Apache-2.0
  README.md
  Makefile                            # new-user workflows (test, run, conformance, ci)
  examples/Caddyfile                  # runnable example used by `make run`
  caddy-dns-records-module.md         # this plan
  dnsrec/
    app.go                            # App, Provider, RecordSpec, RemoveSpec, lifecycle, reconcile
    caddyfile.go                      # parseApp, parseRecord, parseRemove, zone, normalizeProviders,
                                      #   splitProviderSegment, unmarshalModuleTokens
    records.go                        # validation, zone resolution, libdns construction
    app_test.go                       # unit tests with fake provider
    caddyfile_test.go                 # parse tests (dummy provider)
  conformance/
    conformance.go                    # libdnstest wrapper + env config
    smoke_test.go                     # in-memory example smoke (build tag)
    example/
      providers_test.go               # PowerDNS + RFC2136 examples (build tag)
  docker-compose.yml                  # PowerDNS default + BIND/RFC2136 profile
  .docker/pdns/api.conf               # PowerDNS API + webserver config
  .docker/bind/                       # BIND named.conf, TSIG key, zone template
  .github/workflows/ci.yml
  e2e/                                # DEFERRED (placeholder + design notes)
```

Note: the conformance layer needs `libdnstest`, which only exists on `libdns` master
(after v1.1.1). The module therefore pins `github.com/libdns/libdns` to a master
pseudo-version; retarget to a tagged release when one contains `libdnstest`.

---

## 8. Implementation steps

1. Scaffold repo: `go.mod` (`module github.com/SvenDowideit/caddy-host-dns`, Go 1.24+),
   `LICENSE`, `README.md`, `.gitignore`.
2. `dnsrec/app.go`: types, `CaddyModule`, interface guards, `Provision`/`Start`/`Stop`.
3. `dnsrec/records.go`: validation, TTL precedence, zone resolution, `libdns` construction.
4. `dnsrec/caddyfile.go`: `parseApp`, `parseRecords`, `parseRemovals`,
   `normalizeProviders`, `splitProviderSegment`, `unmarshalModuleTokens`.
5. `dnsrec/*_test.go`: fake provider + dummy provider; table tests mirroring
   `caddy-dynamicdns`.
6. `conformance/`: `libdnstest` wrapper, env-config, example `powerdns_test.go`.
7. `docker-compose.yml`: PowerDNS auth + API config for local conformance.
8. `.github/workflows/ci.yml`: build, vet, test, conformance smoke, `xcaddy build`.
9. `README.md`: config examples, provider-agnostic statement, conformance how-to
   (dedicated test zone warning), local PowerDNS instructions.
10. Update this file's status to "implemented" and add a "deferred E2E" tracking issue.

---

## 9. Phasing

- **v1 (this plan)**: module (§4) including `record` create/update, `remove` deletion,
  the `zone` directive, unit + parse tests (§6.1), provider conformance via `libdnstest`
  (§6.2), CI (§6.3), PowerDNS compose aid.
- **v1.1**: clearer provider-capability errors, optional whole-zone authoritative mode
  (see §10).
- **Deferred**: containerized black-box E2E (§6.4); `@svendowideit/caddy` integration
  (§5); additional record types (SRV/TXT/CAA) if a later need arises.

---

## 10. Decisions taken

1. **Conformance record scope**: TXT/A/CNAME are always tested by `libdnstest`; TXT is
   accepted (it proves the provider's generic capability even though the module manages
   only A/AAAA/CNAME). Other types are skipped via `SkipRRTypes`.
2. **Zone requirement**: explicit `zone` (top-level default or per provider/record) wins;
   otherwise derive via `ZoneLister` at Provision; otherwise fail with a clear error
   telling the operator to set `zone`.
3. **`update_only`**: dropped. The module's purpose is to create/update records; the
   owned-RRset `SetRecords` primitive already does create+update. Automatic removal of
   undeclared names is deliberately absent.
4. **Removal**: explicit `remove` directive in v1 (whole `(name,type)` RRset, or specific
   values), re-applied every reconcile and resolved against live records before deletion.
   A future whole-zone authoritative mode remains possible (v1.1).
5. **Dry-run/plan**: dropped. It does not help creation and the fail-loud reconcile plus
   explicit `remove` cover the safety need.
6. **Conformance packaging**: a package inside this module (not a separate module).
7. **Upstream path**: commit now to `github.com/SvenDowideit/caddy-host-dns`; optionally
   offer upstream later behind a re-export if accepted.
