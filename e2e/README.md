# Containerized end-to-end smoke test

A black-box test of the `dns_records` module through Caddy's real Caddyfile and
admin API. Everything runs in Docker on a private network; every stage and every
verification runs through `docker exec`.

## What it does

Three containers:

- **bind** — authoritative BIND for `example.com`, accepting RFC2136 dynamic
  updates over TSIG.
- **caddy** — a Caddy binary built with `dns_records` + a `dns.providers.*`
  module (`rfc2136` by default), attached to two networks so it has two IP
  endpoints.
- **tools** — `dig` + `curl`, used only to observe the system.

Stages (each a `caddy reload` via `docker exec`, then `dig`/`curl` checks):

1. **create** — `alpha` (two A values, one per Caddy endpoint), `beta`, `gamma`.
2. **modify** — prune one of `alpha`'s addresses, change `beta`, add `delta`
   as a CNAME.
3. **delete** — explicitly `remove` `beta` and `gamma`.

HTTP checks pin the response to each endpoint with `curl --resolve`, and each
site `bind`s the exact IP endpoint(s) its DNS records point to, so the response
body proves the name is served from those addresses only.

## Run it

```sh
make e2e            # build Caddy via xcaddy, then run the harness
make e2e-download   # use a caddyserver.com/download binary instead
make e2e-build      # just build e2e/bin/caddy
make e2e-down       # stop containers
```

Directly:

```sh
./e2e/run.sh                    # build (xcaddy) and run
E2E_SKIP_BUILD=1 ./e2e/run.sh   # reuse e2e/bin/caddy
```

## How Caddy is built (and why)

`e2e/build-caddy.sh` runs `xcaddy build` **inside a container**, but bind-mounts
your host Go **module cache and build cache** (`$GOPATH/pkg` and `$GOCACHE`) into
it, so dependencies are not re-downloaded and objects are recompiled only once.
The resulting ~50 MB binary is copied into a small alpine runtime image.

This deliberately uses `docker run` rather than a Dockerfile build: this repo is
tested on a Docker without BuildKit/buildx, where `RUN --mount=type=cache` is
unavailable, and a plain `docker build` of a Go build would freeze gigabytes of
toolchain, module, and compile caches into a throwaway image layer. Building via
`docker run` with `-v` reuses the host caches and keeps images tiny (the caddy
runtime image is ~84 MB, of which ~50 MB is the binary).

For a different platform, set `GOOS`/`GOARCH`; the first cross-build repopulates
the cache and subsequent builds are fast again.

## Files

- `build-caddy.sh` — build `e2e/bin/caddy` (container + host caches).
- `Dockerfile.runtime` — tiny image that copies in the built binary.
- `bind/` — BIND image, `named.conf`, zone, TSIG key.
- `tools/Dockerfile` — `dig` + `curl`.
- `configs/*.caddyfile` — the three stage configs.
- `docker-compose.yml` — the three services and two networks.
- `run.sh` — orchestrator: stages, `docker exec` verification, teardown.
