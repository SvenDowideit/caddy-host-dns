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
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/libdns/libdns"
)

func init() {
	caddy.RegisterModule(fakeProvider{})
	caddy.RegisterModule(setterOnlyProvider{})
}

// fakeProvider is a fully in-memory libdns provider used to unit-test the
// reconcile logic. It records the calls it receives so tests can assert on
// them, and can be configured to fail, to lack a ZoneLister, etc.
type fakeProvider struct {
	Zones    []string `json:"zones,omitempty"`
	Fail     bool     `json:"fail,omitempty"`
	NoLister bool     `json:"no_lister,omitempty"`

	// Live is the set of records returned by GetRecords.
	Live []libdns.Record

	sets    []setCall
	deletes []deleteCall
}

type setCall struct {
	zone string
	recs []libdns.Record
}

type deleteCall struct {
	zone string
	recs []libdns.Record
}

func (fakeProvider) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "dns.providers.test_fake",
		New: func() caddy.Module { return new(fakeProvider) },
	}
}

func (p *fakeProvider) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			return d.Errf("unexpected token %q", d.Val())
		}
	}
	return nil
}

func (p *fakeProvider) ListZones(context.Context) ([]libdns.Zone, error) {
	if p.NoLister {
		return nil, errors.New("not implemented")
	}
	zones := make([]libdns.Zone, len(p.Zones))
	for i, z := range p.Zones {
		zones[i] = libdns.Zone{Name: z}
	}
	return zones, nil
}

func (p *fakeProvider) SetRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if p.Fail {
		return nil, errors.New("boom")
	}
	p.sets = append(p.sets, setCall{zone: zone, recs: recs})
	return recs, nil
}

func (p *fakeProvider) GetRecords(_ context.Context, _ string) ([]libdns.Record, error) {
	if p.Fail {
		return nil, errors.New("boom")
	}
	return p.Live, nil
}

func (p *fakeProvider) DeleteRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if p.Fail {
		return nil, errors.New("boom")
	}
	p.deletes = append(p.deletes, deleteCall{zone: zone, recs: recs})
	return recs, nil
}

// provisionWith parses the given app JSON and provisions it in a real Caddy
// context. The raw JSON is exactly the shape the Caddyfile parser emits.
func provisionWith(t *testing.T, raw string) (*App, error) {
	t.Helper()
	var app App
	if err := caddy.StrictUnmarshalJSON([]byte(raw), &app); err != nil {
		t.Fatalf("unmarshal app JSON: %v", err)
	}
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	t.Cleanup(cancel)
	if err := app.Provision(ctx); err != nil {
		return &app, err
	}
	return &app, nil
}

func Test_Provision_Reconcile(t *testing.T) {
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy.", "example.com."]},
			"records": [
				{"name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5", "10.10.0.6"]},
				{"name": "obs.fi.gy", "type": "A", "value": ["10.10.0.7"], "ttl": "5m"},
				{"name": "v6.fi.gy", "type": "AAAA", "value": ["2001:db8::1"]},
				{"name": "gateway.fi.gy", "type": "CNAME", "value": ["otel.fi.gy."]}
			],
			"remove": [
				{"name": "stale.fi.gy", "type": "A"},
				{"name": "stale2.fi.gy", "type": "A", "value": ["10.10.0.99"]}
			]
		}],
		"ttl": "1h"
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	app.Providers[0].getter.(*fakeProvider).Live = []libdns.Record{
		libdns.Address{Name: "stale", TTL: 7 * time.Hour, IP: netip.MustParseAddr("10.10.0.1")},
		libdns.Address{Name: "stale", TTL: 7 * time.Hour, IP: netip.MustParseAddr("10.10.0.2")},
		libdns.Address{Name: "stale2", TTL: 7 * time.Hour, IP: netip.MustParseAddr("10.10.0.99")},
		libdns.Address{Name: "stale2", TTL: 7 * time.Hour, IP: netip.MustParseAddr("10.10.0.98")},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake := app.Providers[0].dnsProvider.(*fakeProvider)

	if len(fake.sets) != 1 {
		t.Fatalf("expected 1 SetRecords call, got %d", len(fake.sets))
	}
	set := fake.sets[0]
	if set.zone != "fi.gy." {
		t.Errorf("set zone = %q, want fi.gy.", set.zone)
	}

	addr, ok := set.recs[0].(libdns.Address)
	if !ok {
		t.Fatalf("record 0 = %T, want libdns.Address", set.recs[0])
	}
	if addr.Name != "otel" || addr.TTL != time.Hour {
		t.Errorf("otel: name=%q ttl=%v, want otel / 1h", addr.Name, addr.TTL)
	}
	if addr.IP.String() != "10.10.0.5" {
		t.Errorf("otel ip = %s", addr.IP)
	}
	addr2 := set.recs[1].(libdns.Address)
	if addr2.IP.String() != "10.10.0.6" {
		t.Errorf("otel second ip = %s", addr2.IP)
	}
	obs := set.recs[2].(libdns.Address)
	if obs.TTL != 5*time.Minute {
		t.Errorf("obs ttl = %v, want 5m (per-record override)", obs.TTL)
	}
	v6 := set.recs[3].(libdns.Address)
	if v6.RR().Type != "AAAA" || v6.IP.String() != "2001:db8::1" {
		t.Errorf("v6 = %+v", v6)
	}
	cname := set.recs[4].(libdns.CNAME)
	if cname.Name != "gateway" || cname.Target != "otel.fi.gy." {
		t.Errorf("cname = %+v", cname)
	}

	if len(fake.deletes) != 1 {
		t.Fatalf("expected 1 DeleteRecords call, got %d", len(fake.deletes))
	}
	del := fake.deletes[0]
	if del.zone != "fi.gy." {
		t.Errorf("delete zone = %q", del.zone)
	}
	if len(del.recs) != 3 {
		t.Fatalf("expected 3 delete records, got %d: %+v", len(del.recs), del.recs)
	}
	rr0 := del.recs[0].RR()
	if rr0.Name != "stale" || rr0.Type != "A" || rr0.Data != "10.10.0.1" {
		t.Errorf("delete[0] = %+v, want name=stale type=A data=10.10.0.1", rr0)
	}
	rr1 := del.recs[1].RR()
	if rr1.Name != "stale" || rr1.Data != "10.10.0.2" {
		t.Errorf("delete[1] = %+v, want name=stale data=10.10.0.2", rr1)
	}
	rr2 := del.recs[2].RR()
	if rr2.Name != "stale2" || rr2.Data != "10.10.0.99" {
		t.Errorf("delete[2] = %+v, want name=stale2 data=10.10.0.99", rr2)
	}
}

func Test_Provision_Errors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unsupported type",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake"},"records":[{"name":"a.fi.gy","type":"TXT","value":["x"]}]}]}`,
		},
		{
			name: "A with IPv6 value",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake"},"records":[{"name":"a.fi.gy","type":"A","value":["2001:db8::1"]}]}]}`,
		},
		{
			name: "AAAA with IPv4 value",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake"},"records":[{"name":"a.fi.gy","type":"AAAA","value":["10.0.0.1"]}]}]}`,
		},
		{
			name: "CNAME with two targets",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake"},"records":[{"name":"a.fi.gy","type":"CNAME","value":["x.","y."]}]}]}`,
		},
		{
			name: "CNAME target is an IP",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake"},"records":[{"name":"a.fi.gy","type":"CNAME","value":["10.0.0.1"]}]}]}`,
		},
		{
			name: "missing name",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake"},"records":[{"type":"A","value":["10.0.0.1"]}]}]}`,
		},
		{
			name: "no zone and no lister",
			raw:  `{"providers":[{"dns_provider":{"name":"test_fake","no_lister":true},"records":[{"name":"a.fi.gy","type":"A","value":["10.0.0.1"]}]}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := provisionWith(t, tt.raw); err == nil {
				t.Fatal("expected Provision error, got nil")
			}
		})
	}
}

func Test_Provision_RemoveRequiresDeleter(t *testing.T) {
	if _, ok := any(&fakeProvider{}).(libdns.RecordDeleter); !ok {
		t.Fatal("fakeProvider must implement RecordDeleter")
	}
	_, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_setter_only", "zones": ["fi.gy."]},
			"remove": [{"name": "stale.fi.gy", "type": "A"}]
		}]
	}`)
	if err == nil {
		t.Fatal("expected error when a remove directive is used with a provider lacking RecordDeleter")
	}
}

// setterOnlyProvider implements only libdns.RecordSetter, not RecordDeleter.
type setterOnlyProvider struct {
	Zones []string `json:"zones,omitempty"`
}

func (setterOnlyProvider) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "dns.providers.test_setter_only",
		New: func() caddy.Module { return new(setterOnlyProvider) },
	}
}

func (p *setterOnlyProvider) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			return d.Errf("unexpected token %q", d.Val())
		}
	}
	return nil
}

func (p *setterOnlyProvider) ListZones(context.Context) ([]libdns.Zone, error) {
	zones := make([]libdns.Zone, len(p.Zones))
	for i, z := range p.Zones {
		zones[i] = libdns.Zone{Name: z}
	}
	return zones, nil
}

func (p *setterOnlyProvider) SetRecords(context.Context, string, []libdns.Record) ([]libdns.Record, error) {
	return nil, nil
}

func Test_Start_BestEffort(t *testing.T) {
	raw := `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."], "fail": true},
			"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.0.0.1"]}]
		}]
	}`
	// fail hard by default
	app, err := provisionWith(t, raw)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err == nil {
		t.Error("expected Start error without best_effort")
	}

	// succeed with best_effort
	app, err = provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."], "fail": true},
			"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.0.0.1"]}]
		}],
		"best_effort": true
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Errorf("Start with best_effort returned error: %v", err)
	}
}

func Test_Provision_ProviderZone(t *testing.T) {
	// A provider without ZoneLister can still be used when a zone is given.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "no_lister": true},
			"zone": "fi.gy",
			"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.0.0.1"]}]
		}]
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := app.Providers[0].Records[0].Zone; got != "fi.gy." {
		t.Errorf("record zone = %q, want fi.gy.", got)
	}
}

func Test_LongestZone(t *testing.T) {
	zones := []libdns.Zone{{Name: "example.com."}, {Name: "sub.example.com."}, {Name: "other.net."}}
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{"a.sub.example.com", "sub.example.com.", true},
		{"a.example.com", "example.com.", true},
		{"example.com", "example.com.", true},
		{"a.other.net", "other.net.", true},
		{"nope.invalid", "", false},
	}
	for _, tt := range tests {
		got, ok := longestZone(zones, tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("longestZone(%q) = %q,%v want %q,%v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}
