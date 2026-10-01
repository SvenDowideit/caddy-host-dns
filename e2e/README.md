# Deferred: containerized black-box Caddy + PowerDNS E2E

This directory is a placeholder for the **deferred** end-to-end harness described in
[`../caddy-dns-records-module.md`](../caddy-dns-records-module.md) §6.4.

It is intentionally empty of code. The design is decided but the orchestration choice
(testcontainers-go vs Makefile + docker compose vs a standalone runner) is left open.

When built, it should:

- build a Caddy image with a build arg, e.g.
  `xcaddy build --with github.com/SvenDowideit/caddy-host-dns --with github.com/caddy-dns/<provider>`;
- run Caddy and PowerDNS on a shared compose network, with a throwaway `dig` sidecar;
- generate Caddyfiles using `dns_records { provider powerdns ... record ... }`, seed a
  synthetic zone, and assert via `dig` and `docker exec`: A/AAAA/CNAME create, idempotent
  reload, prune on value change, multi-value A, validation error fails config, auth error
  is reported clearly, TTL applied, and zone auto-detect.
