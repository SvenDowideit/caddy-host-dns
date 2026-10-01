// Copyright 2026 Sven Dowideit
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build conformance

// Package conformance_test runs the dns_records provider conformance wrapper
// against local/real providers. Each test skips unless its provider-specific
// environment variable is set, so `go test -tags conformance ./...` only runs
// the in-memory smoke test, while the Makefile targets set the variables.
//
// Local PowerDNS (see docker compose + README), exercising A/AAAA/CNAME:
//
//	make conformance-live
//
// Local BIND with RFC2136 dynamic updates (passes the full suite):
//
//	make conformance-bind
//
// WARNING: the suite creates and deletes records named "test-*". Only ever
// point these at a dedicated test zone.
package conformance_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/SvenDowideit/caddy-host-dns/conformance"
)

// providerJSON builds a dns_provider object for conformance.Run.
func providerJSON(name string, fields map[string]string) json.RawMessage {
	obj := map[string]string{"name": name}
	for k, v := range fields {
		obj[k] = v
	}
	b, err := json.Marshal(obj)
	if err != nil {
		panic(fmt.Sprintf("building provider JSON: %v", err))
	}
	return b
}

// zone returns CONFORMANCE_ZONE, defaulting to example.com.
func zone() string {
	if z := os.Getenv("CONFORMANCE_ZONE"); z != "" {
		return z
	}
	return "example.com."
}

// TestPowerDNS runs the suite against a local PowerDNS when CONFORMANCE_PDNS_URL
// is set (e.g. via `make conformance-live`).
//
// NOTE: this currently FAILS on the suite's TXT cases because of a known bug in
// the upstream libdns/powerdns provider: its read path never unquotes TXT, so
// GetRecords returns values wrapped in extra quotes. The A, AAAA, and CNAME
// cases all pass, and dns_records itself only manages those types. See README.
func TestPowerDNS(t *testing.T) {
	url := os.Getenv("CONFORMANCE_PDNS_URL")
	if url == "" {
		t.Skip("set CONFORMANCE_PDNS_URL to run PowerDNS conformance (make conformance-live)")
	}
	provider := providerJSON("powerdns", map[string]string{
		"server_url": url,
		"api_token":  os.Getenv("CONFORMANCE_PDNS_TOKEN"),
	})
	conformance.Run(t, provider, zone())
}

// TestBindRFC2136 runs the suite against a local BIND server using RFC2136
// dynamic updates over TSIG when CONFORMANCE_RFC2136_SERVER is set (e.g. via
// `make conformance-bind`). libdns/rfc2136 handles TXT correctly, so this
// passes the full suite (A, AAAA, CNAME, and TXT).
func TestBindRFC2136(t *testing.T) {
	server := os.Getenv("CONFORMANCE_RFC2136_SERVER")
	if server == "" {
		t.Skip("set CONFORMANCE_RFC2136_SERVER to run RFC2136 conformance (make conformance-bind)")
	}
	provider := providerJSON("rfc2136", map[string]string{
		"key_name": os.Getenv("CONFORMANCE_RFC2136_KEYNAME"),
		"key_alg":  os.Getenv("CONFORMANCE_RFC2136_KEYALG"),
		"key":      os.Getenv("CONFORMANCE_RFC2136_KEY"),
		"server":   server,
	})
	conformance.Run(t, provider, zone())
}
