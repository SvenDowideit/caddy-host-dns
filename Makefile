# dns_records — developer Makefile
#
# New here? Start with:
#   make help              # list every target
#   make test              # unit + Caddyfile parse tests (no network)
#   make conformance       # provider conformance smoke (in-memory, no creds)
#   make run               # build Caddy + module and try examples/Caddyfile
#   make conformance-live  # local PowerDNS: start, seed, run conformance
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
CONFORMANCE_PROVIDER_JSON ?= {"name":"powerdns","server_url":"$(PDNS_URL)","api_token":"$(PDNS_KEY)"}
CONFORMANCE_ZONE ?= $(TEST_ZONE)

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

# --- conformance against a live provider -----------------------------------

.PHONY: conformance-live
conformance-live: pdns-up pdns-seed ## Run conformance against local PowerDNS
	@echo "Note: some providers (e.g. upstream libdns/powerdns) fail the suite's"
	@echo "TXT cases; dns_records itself only manages A/AAAA/CNAME."
	@echo
	CONFORMANCE_PROVIDER_JSON='$(CONFORMANCE_PROVIDER_JSON)' \
	CONFORMANCE_ZONE='$(CONFORMANCE_ZONE)' \
		$(GO) test $(GOFLAGS) -tags conformance -count=1 -v ./conformance/example/...
