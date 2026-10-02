# dns_records — developer Makefile
#
# New here? Start with:
#   make help              # list every target
#   make test              # unit + Caddyfile parse tests (no network)
#   make conformance       # provider conformance smoke (in-memory, no creds)
#   make run               # build Caddy + module and try examples/Caddyfile
#   make conformance-live  # local PowerDNS: start, seed, run conformance
#   make conformance-bind  # local BIND/RFC2136: full suite, passes
#   make e2e               # containerized BIND e2e (xcaddy-built Caddy)
#   make e2e-download      # containerized BIND e2e (caddyserver.com Caddy)
#   make ci                # everything CI runs

# --- configuration ---------------------------------------------------------

MODULE     := github.com/SvenDowideit/caddy-host-dns

# Any dns.providers.* module to compile in, e.g. github.com/caddy-dns/cloudflare
PROVIDER   ?= github.com/caddy-dns/powerdns

GO         ?= go
GOFLAGS    ?=
CADDY_BIN  ?= ./caddy
EXAMPLE    ?= examples/Caddyfile

# Prefer an xcaddy on PATH, else the one in GOPATH/bin.
XCADDY ?= $(shell command -v xcaddy 2>/dev/null || $(GO) env GOPATH)/bin/xcaddy

# Local PowerDNS (see docker-compose.yml)
PDNS_URL   ?= http://127.0.0.1:8081
PDNS_KEY   ?= secret
TEST_ZONE  ?= example.com.

# Local BIND (RFC2136 dynamic updates over TSIG)
BIND_SERVER ?= 127.0.0.1:5354
BIND_KEYNAME ?= rfc2136-key
BIND_KEYALG  ?= hmac-sha256
BIND_KEY     ?= cWnu6Ju9zOki4f7Q+da2KKGo0KOXbCf6Pej6hW3geC4=

# --- meta ------------------------------------------------------------------

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "dns_records — available targets:"
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Variables: PROVIDER=$(PROVIDER)  CADDY_BIN=$(CADDY_BIN)  TEST_ZONE=$(TEST_ZONE)"

# --- build & test ----------------------------------------------------------

.PHONY: build
build: ## Compile all packages
	$(GO) build $(GOFLAGS) ./...

.PHONY: test
test: ## Run unit + Caddyfile parse tests
	$(GO) test $(GOFLAGS) ./...

.PHONY: conformance
conformance: ## Run provider conformance smoke test (in-memory provider)
	$(GO) test $(GOFLAGS) -tags conformance -count=1 ./conformance/...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(GOFLAGS) ./...

.PHONY: fmt
fmt: ## Format all Go files
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt'd
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	$(GO) mod tidy

.PHONY: ci
ci: fmt-check vet build test conformance ## Run the full CI suite locally

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(CADDY_BIN)

# --- download a Caddy binary from caddyserver.com/download ------------------

# Build a Caddy binary using the hosted download service. NOTE: the service
# only builds package paths listed in its registry
# (https://caddyserver.com/api/packages). Until this module is registered there,
# the service returns HTTP 400 "is not a registered Caddy module package path".
# Register at https://caddyserver.com/account/register-package, or use
# `make xcaddy` / `make e2e` (the xcaddy build has no such restriction).
DOWNLOAD_CADDY ?= caddy-download

.PHONY: download
download: ## Download Caddy from caddyserver.com/download with dns_records + PROVIDER
	@url="https://caddyserver.com/api/download?os=linux&arch=amd64"; \
	url="$$url&p=$(MODULE)"; \
	url="$$url&p=$(PROVIDER)"; \
	echo "GET $$url"; \
	curl -fsSL "$$url" -o $(DOWNLOAD_CADDY) || { \
		echo; \
		echo "Download failed. The caddyserver.com build service only builds"; \
		echo "module paths in its registry (https://caddyserver.com/api/packages)."; \
		echo "If $(MODULE) is not registered yet, use 'make xcaddy' instead."; \
		exit 1; \
	}; \
	chmod +x $(DOWNLOAD_CADDY); \
	$(DOWNLOAD_CADDY) version; \
	$(DOWNLOAD_CADDY) list-modules | grep -E 'dns_records|dns\.providers\.' || true

# --- build a real Caddy with the module ------------------------------------

GO_SOURCES := go.mod go.sum $(shell find . -name '*.go' -not -path './.git/*' 2>/dev/null)

# The Caddy binary is a real file target: it is rebuilt only when Go sources
# change, so `make run` does not rebuild on every invocation.
$(CADDY_BIN): $(GO_SOURCES)
	@test -x "$(XCADDY)" || { \
		echo "xcaddy not found. Install it with:"; \
		echo "  $(GO) install github.com/caddyserver/xcaddy/cmd/xcaddy@latest"; \
		exit 1; \
	}
	$(XCADDY) build --output $(CADDY_BIN) --with $(MODULE)=. --with $(PROVIDER)
	@echo "built $(CADDY_BIN)"
	@$(CADDY_BIN) list-modules | grep -E 'dns_records|dns\.providers\.' || true

.PHONY: xcaddy
xcaddy: ## Rebuild Caddy (./caddy) with dns_records + PROVIDER
	rm -f $(CADDY_BIN)
	$(MAKE) $(CADDY_BIN)

.PHONY: run
run: pdns-up pdns-seed $(CADDY_BIN) ## Try examples/Caddyfile against local PowerDNS
	$(CADDY_BIN) run --adapter caddyfile --config $(EXAMPLE)

# --- local PowerDNS (docker compose) ---------------------------------------

.PHONY: pdns-up
pdns-up: ## Start local PowerDNS and wait for its API
	docker compose up -d
	@printf "waiting for PowerDNS API"; \
	for i in $$(seq 1 30); do \
		if curl -fsS -H 'X-API-Key: $(PDNS_KEY)' $(PDNS_URL)/api/v1/servers/localhost/zones >/dev/null 2>&1; then \
			echo " ready"; exit 0; \
		fi; \
		printf "."; sleep 1; \
	done; \
	echo " timed out"; exit 1

.PHONY: pdns-seed
pdns-seed: ## Create the local PowerDNS test zone
	@curl -fsS -X POST -H 'X-API-Key: $(PDNS_KEY)' -H 'Content-Type: application/json' \
		$(PDNS_URL)/api/v1/servers/localhost/zones \
		-d '{"name":"$(TEST_ZONE)","kind":"Native","nameservers":["ns1.$(TEST_ZONE)"]}' >/dev/null 2>&1 \
		&& echo "created zone $(TEST_ZONE)" \
		|| echo "zone $(TEST_ZONE) already exists (ok)"

.PHONY: pdns-logs
pdns-logs: ## Follow local PowerDNS logs
	docker compose logs -f powerdns

.PHONY: pdns-down
pdns-down: ## Stop local PowerDNS and remove its data
	docker compose down -v

# --- conformance against live local providers ------------------------------

.PHONY: conformance-live
conformance-live: pdns-up pdns-seed ## Run conformance against local PowerDNS
	@echo "==========================================================================="
	@echo " Running the libdns conformance suite against local PowerDNS."
	@echo
	@echo " EXPECTED FAILURE: the TXT cases fail due to a known bug in the upstream"
	@echo " libdns/powerdns provider (its read path returns TXT values wrapped in"
	@echo " extra quotes). The A, AAAA, and CNAME cases pass, and dns_records itself"
	@echo " only manages A/AAAA/CNAME. For a fully green run, use: make conformance-bind"
	@echo "==========================================================================="
	@echo
	CONFORMANCE_ZONE='$(TEST_ZONE)' \
	CONFORMANCE_PDNS_URL='$(PDNS_URL)' \
	CONFORMANCE_PDNS_TOKEN='$(PDNS_KEY)' \
		$(GO) test $(GOFLAGS) -tags conformance -count=1 -v -run TestPowerDNS ./conformance/example/...

.PHONY: conformance-bind
conformance-bind: bind-up ## Run conformance against local BIND (RFC2136); passes fully
	CONFORMANCE_ZONE='$(TEST_ZONE)' \
	CONFORMANCE_RFC2136_SERVER='$(BIND_SERVER)' \
	CONFORMANCE_RFC2136_KEYNAME='$(BIND_KEYNAME)' \
	CONFORMANCE_RFC2136_KEYALG='$(BIND_KEYALG)' \
	CONFORMANCE_RFC2136_KEY='$(BIND_KEY)' \
		$(GO) test $(GOFLAGS) -tags conformance -count=1 -v -run TestBindRFC2136 ./conformance/example/...

# --- local BIND (RFC2136) --------------------------------------------------

.PHONY: bind-up
bind-up: ## Start local BIND and wait for DNS
	@mkdir -p .docker/bind/zones
	@test -f .docker/bind/zones/db.example.com || cp .docker/bind/db.example.com.template .docker/bind/zones/db.example.com
	@chmod -R a+rwX .docker/bind/zones
	docker compose --profile bind up -d bind
	@printf "waiting for BIND"; \
	for i in $$(seq 1 30); do \
		if dig +short +time=1 +tries=1 @127.0.0.1 -p 5354 $(TEST_ZONE) SOA >/dev/null 2>&1; then \
			echo " ready"; exit 0; \
		fi; \
		printf "."; sleep 1; \
	done; \
	echo " timed out"; docker compose --profile bind logs --no-color bind; exit 1

.PHONY: bind-logs
bind-logs: ## Follow local BIND logs
	docker compose --profile bind logs -f bind

.PHONY: bind-down
bind-down: ## Stop local BIND and remove its data
	docker compose --profile bind down -v
	rm -rf .docker/bind/zones

# --- containerized end-to-end smoke test -----------------------------------

# Builds Caddy with e2e/build-caddy.sh (inside a container, but reusing your Go
# module/build caches via bind mounts, so nothing is re-downloaded), runs BIND +
# Caddy + a tools container on a private network, then drives create/modify/
# delete stages and verifies each with dig and curl via `docker exec`.
E2E_PROVIDER ?= github.com/caddy-dns/rfc2136

.PHONY: e2e
e2e: ## Run the containerized BIND e2e (builds Caddy via xcaddy in a container)
	E2E_PROVIDER=$(E2E_PROVIDER) ./e2e/run.sh

.PHONY: e2e-build
e2e-build: ## Build e2e/bin/caddy only (reusing host Go caches)
	E2E_PROVIDER=$(E2E_PROVIDER) ./e2e/build-caddy.sh

.PHONY: e2e-caddyserver
e2e-caddyserver: ## Run the e2e with a Caddy downloaded from caddyserver.com/download
	E2E_PROVIDER=$(E2E_PROVIDER) ./e2e/fetch-caddy.sh
	E2E_SKIP_BUILD=1 E2E_PROVIDER=$(E2E_PROVIDER) ./e2e/run.sh

.PHONY: e2e-download
e2e-download: e2e-caddyserver ## Alias for e2e-caddyserver

.PHONY: e2e-config
e2e-config: ## Validate the e2e compose file
	docker compose -f e2e/docker-compose.yml config >/dev/null && echo "e2e compose OK"

.PHONY: e2e-down
e2e-down: ## Stop the e2e containers and network
	docker compose -f e2e/docker-compose.yml down -v --remove-orphans

.PHONY: e2e-clean
e2e-clean: ## Remove the e2e-built Caddy binary
	rm -rf e2e/bin
