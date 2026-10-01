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

// Package conformance_test is a worked example of running provider conformance
// tests against a real provider compiled into this test binary.
//
// Start the local PowerDNS via `docker compose up -d`, seed a zone, then:
//
//	CONFORMANCE_PROVIDER_JSON='{"name":"powerdns","server_url":"http://127.0.0.1:8081","api_token":"secret"}' \
//	CONFORMANCE_ZONE=example.com. \
//	go test -tags conformance -v ./conformance/example/...
//
// WARNING: the suite creates and deletes records named "test-*". Only ever
// point this at a dedicated test zone.
package conformance_test

import (
	"testing"

	"github.com/SvenDowideit/caddy-host-dns/conformance"
	_ "github.com/caddy-dns/powerdns"
)

func TestPowerDNS(t *testing.T) {
	conformance.RunFromEnv(t)
}
