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

// Package conformance runs the official libdns provider test suite against a
// libdns provider, proving that the provider, its API settings, and a zone all
// work together.
//
// The provider must be compiled into the test binary. The easiest way is to
// import the Caddy DNS provider module (for example
// github.com/caddy-dns/powerdns) in your test file and call RunFromEnv:
//
//	//go:build conformance
//
//	package conformance_test
//
//	import (
//		"testing"
//
//		"github.com/SvenDowideit/caddy-host-dns/conformance"
//		_ "github.com/caddy-dns/powerdns"
//	)
//
//	func TestProvider(t *testing.T) {
//		conformance.RunFromEnv(t)
//	}
//
// Then run, with a dedicated test zone:
//
//	CONFORMANCE_PROVIDER_JSON='{"name":"powerdns","server_url":"http://127.0.0.1:8081","api_token":"secret"}' \
//	CONFORMANCE_ZONE=example.com. \
//	go test -tags conformance -v ./...
//
// WARNING: the suite creates and deletes records named "test-*". Always use a
// dedicated test zone; your DNS records may be deleted or overwritten.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/libdns/libdns/libdnstest"
)

// DefaultSkipRRTypes lists the record types not exercised by default. A, AAAA,
// CNAME, and TXT are always tested (TXT is used by the test framework itself).
var DefaultSkipRRTypes = map[string]bool{
	"MX":    true,
	"SRV":   true,
	"CAA":   true,
	"NS":    true,
	"SVCB":  true,
	"HTTPS": true,
}

// RunFromEnv runs the conformance suite using the provider JSON in
// CONFORMANCE_PROVIDER_JSON and the zone in CONFORMANCE_ZONE. It skips the test
// with a clear message if either is unset.
func RunFromEnv(t *testing.T) {
	t.Helper()
	raw := os.Getenv("CONFORMANCE_PROVIDER_JSON")
	zone := os.Getenv("CONFORMANCE_ZONE")
	if raw == "" || zone == "" {
		t.Skip("set CONFORMANCE_PROVIDER_JSON and CONFORMANCE_ZONE to run provider conformance tests")
	}
	Run(t, json.RawMessage(raw), zone)
}

// Run loads the dns.providers.* module named in providerJSON (the same object
// that goes under "dns_provider" in the Caddy config) and runs the conformance
// suite against the given zone. The module must be compiled into the binary.
func Run(t *testing.T, providerJSON json.RawMessage, zone string) {
	t.Helper()

	provider, err := loadProvider(providerJSON)
	if err != nil {
		t.Fatalf("loading provider module: %v", err)
	}

	rp, ok := provider.(libdnstest.RecordProvider)
	if !ok {
		t.Fatalf("provider %T does not implement the required libdns interfaces (RecordGetter, RecordAppender, RecordSetter, RecordDeleter)", provider)
	}
	RunRecordProvider(t, rp, zone)
}

// RunRecordProvider runs the conformance suite against a provider that
// implements the core record interfaces but not necessarily ZoneLister.
func RunRecordProvider(t *testing.T, provider libdnstest.RecordProvider, zone string) {
	t.Helper()
	RunProvider(t, libdnstest.WrapNoZoneLister(provider), zone)
}

// RunProvider runs the conformance suite against a fully-featured provider.
// Use this when you construct the provider yourself instead of loading it from
// a Caddy module.
func RunProvider(t *testing.T, provider libdnstest.Provider, zone string) {
	t.Helper()
	if zone == "" {
		t.Fatal("conformance zone must not be empty")
	}
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	suite := libdnstest.NewTestSuite(provider, zone)
	suite.SkipRRTypes = DefaultSkipRRTypes
	suite.ExpectEmptyZone = true
	suite.RunTests(t)
}

// loadProvider loads a dns.providers.* module from JSON containing a "name"
// field, in a provisioned Caddy context.
func loadProvider(raw json.RawMessage) (any, error) {
	var name struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &name); err != nil {
		return nil, fmt.Errorf("parsing provider JSON: %w", err)
	}
	if name.Name == "" {
		return nil, fmt.Errorf("provider JSON is missing the \"name\" field")
	}

	wrapper := struct {
		DNSProviderRaw json.RawMessage `json:"dns_provider,omitempty" caddy:"namespace=dns.providers inline_key=name"`
	}{DNSProviderRaw: raw}

	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()

	val, err := ctx.LoadModule(&wrapper, "DNSProviderRaw")
	if err != nil {
		return nil, err
	}
	return val, nil
}
