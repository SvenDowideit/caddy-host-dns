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

package dnsrec

import (
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// soaSets returns the SOA records the fake provider was asked to set.
func soaSets(fake *fakeProvider) []libdns.RR {
	var out []libdns.RR
	for _, call := range fake.sets {
		for _, rec := range call.recs {
			if rr := rec.RR(); strings.EqualFold(rr.Type, "SOA") {
				out = append(out, rr)
			}
		}
	}
	return out
}

func Test_SOA_NotConfiguredLeavesZoneAlone(t *testing.T) {
	// Without a soa block the app must never write an SOA.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"records": [{"name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5"]}]
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := soaSets(app.Providers[0].dnsProvider.(*fakeProvider)); len(got) != 0 {
		t.Fatalf("unexpected SOA write: %+v", got)
	}
}

func Test_SOA_CreatesWithSmallDefaults(t *testing.T) {
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"soa": {"mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy.", "zone": "fi.gy"}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sets := soaSets(app.Providers[0].dnsProvider.(*fakeProvider))
	if len(sets) != 1 {
		t.Fatalf("expected 1 SOA set, got %d", len(sets))
	}
	rr := sets[0]
	if rr.Name != "@" || rr.TTL != time.Minute {
		t.Errorf("SOA name/ttl = %q/%v, want @/1m", rr.Name, rr.TTL)
	}
	f, ok := parseSOAData(rr.Data)
	if !ok {
		t.Fatalf("could not parse SOA data %q", rr.Data)
	}
	if f.mname != "ns1.fi.gy." || f.rname != "hostmaster.fi.gy." {
		t.Errorf("mname/rname = %q/%q", f.mname, f.rname)
	}
	if f.refresh != 300 || f.retry != 300 || f.expire != 600 || f.minimum != 60 {
		t.Errorf("timers = %d/%d/%d/%d, want 300/300/600/60", f.refresh, f.retry, f.expire, f.minimum)
	}
	if f.serial == 0 {
		t.Errorf("serial should default to a non-zero value, got 0")
	}
}

func Test_SOA_InheritsFromLiveAndIsIdempotent(t *testing.T) {
	// When the provider returns the SOA, mname/rname/serial are inherited and
	// an unchanged SOA causes no write.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"soa": {
				"zone": "fi.gy",
				"refresh": "5m", "retry": "5m", "expire": "10m",
				"minimum": "1m", "ttl": "1m"
			}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	app.Providers[0].getter.(*fakeProvider).Live = []libdns.Record{
		libdns.RR{Name: "@", TTL: time.Minute, Type: "SOA",
			Data: "ns1.fi.gy. hostmaster.fi.gy. 7 300 300 600 60"},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := soaSets(app.Providers[0].dnsProvider.(*fakeProvider)); len(got) != 0 {
		t.Fatalf("unchanged SOA was rewritten: %+v", got)
	}
}

func Test_SOA_SerialIncrementsOnChange(t *testing.T) {
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"soa": {"zone": "fi.gy", "minimum": "30s"}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	app.Providers[0].getter.(*fakeProvider).Live = []libdns.Record{
		libdns.RR{Name: "@", TTL: time.Minute, Type: "SOA",
			Data: "ns1.fi.gy. hostmaster.fi.gy. 7 300 300 600 3600"},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sets := soaSets(app.Providers[0].dnsProvider.(*fakeProvider))
	if len(sets) != 1 {
		t.Fatalf("expected 1 SOA set, got %d", len(sets))
	}
	f, _ := parseSOAData(sets[0].Data)
	if f.serial != 8 {
		t.Errorf("serial = %d, want 8 (live 7 incremented)", f.serial)
	}
	if f.mname != "ns1.fi.gy." || f.rname != "hostmaster.fi.gy." {
		t.Errorf("mname/rname not inherited: %q/%q", f.mname, f.rname)
	}
	if f.minimum != 30 {
		t.Errorf("minimum = %d, want 30", f.minimum)
	}
}

func Test_SOA_ExplicitSerialAndMinTTLFloor(t *testing.T) {
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"soa": {"zone": "fi.gy", "mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy.", "serial": 1234, "minimum": "30s", "min_ttl": "1m"}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sets := soaSets(app.Providers[0].dnsProvider.(*fakeProvider))
	if len(sets) != 1 {
		t.Fatalf("expected 1 SOA set, got %d", len(sets))
	}
	f, _ := parseSOAData(sets[0].Data)
	if f.serial != 1234 {
		t.Errorf("serial = %d, want 1234", f.serial)
	}
	if f.minimum != 60 {
		t.Errorf("minimum = %d, want 60 (raised to min_ttl)", f.minimum)
	}
	if sets[0].TTL != time.Minute {
		t.Errorf("ttl = %v, want 1m (raised to min_ttl)", sets[0].TTL)
	}
}

func Test_SOA_RequiresNamesWithoutGetter(t *testing.T) {
	_, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_setter_only", "zones": ["fi.gy."]},
			"soa": {"zone": "fi.gy"}
		}]
	}`)
	if err == nil {
		t.Fatal("expected error: mname/rname must be set when the provider has no RecordGetter")
	}

	if _, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_setter_only", "zones": ["fi.gy."]},
			"soa": {"zone": "fi.gy", "mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy."}
		}]
	}`); err != nil {
		t.Fatalf("Provision with explicit names: %v", err)
	}
}

func Test_SOA_RequiresUnambiguousZone(t *testing.T) {
	_, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy.", "other.net."]},
			"records": [
				{"name": "a.fi.gy", "type": "A", "value": ["10.0.0.1"]},
				{"name": "b.other.net", "type": "A", "value": ["10.0.0.2"]}
			],
			"soa": {"mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy."}
		}]
	}`)
	if err == nil {
		t.Fatal("expected error: soa zone is ambiguous across two zones")
	}

	// A single listed zone is inferred when the provider lists exactly one.
	if _, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"soa": {"mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy."}
		}]
	}`); err != nil {
		t.Fatalf("Provision with single inferred zone: %v", err)
	}
}

func Test_SOA_WritesAreAdvisory(t *testing.T) {
	// A provider that rejects the SOA write must not fail the reconcile: SOA
	// management is best-attempt only.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."], "fail_set": true},
			"soa": {"zone": "fi.gy", "mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy."}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start should not fail when the SOA write is rejected: %v", err)
	}
}

func Test_SOA_BuildFailureIsAdvisory(t *testing.T) {
	// The provider has a RecordGetter but returns no SOA, and no mname/rname is
	// configured: the module should advise and continue, not fail.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"soa": {"zone": "fi.gy"}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start should not fail when the SOA cannot be built: %v", err)
	}
	if got := soaSets(app.Providers[0].dnsProvider.(*fakeProvider)); len(got) != 0 {
		t.Fatalf("no SOA should have been written: %+v", got)
	}
}

func Test_SOA_SharedRecordZoneInferred(t *testing.T) {
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy.", "other.net."]},
			"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.0.0.1"]}],
			"soa": {"mname": "ns1.fi.gy.", "rname": "hostmaster.fi.gy."}
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := app.Providers[0].SOA.Zone; got != "fi.gy." {
		t.Errorf("soa zone = %q, want fi.gy. (inferred from the sole record)", got)
	}
}
