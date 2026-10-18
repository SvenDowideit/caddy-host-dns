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
	FailSet  bool     `json:"fail_set,omitempty"`
	NoLister bool     `json:"no_lister,omitempty"`

	// Live is the set of records returned by GetRecords.
	Live []libdns.Record

	sets    []setCall
	appends []setCall
	deletes []deleteCall
	ops     []string
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
	if p.Fail || p.FailSet {
		return nil, errors.New("boom")
	}
	p.sets = append(p.sets, setCall{zone: zone, recs: recs})
	p.ops = append(p.ops, "set")
	return recs, nil
}

func (p *fakeProvider) AppendRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	if p.Fail {
		return nil, errors.New("boom")
	}
	p.appends = append(p.appends, setCall{zone: zone, recs: recs})
	p.ops = append(p.ops, "append")
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
	p.ops = append(p.ops, "delete")
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

	// Fresh create appends the A/AAAA values and sets the CNAME; the stale
	// removal below uses DeleteRecords.
	if len(fake.appends) != 1 {
		t.Fatalf("expected 1 AppendRecords call, got %d", len(fake.appends))
	}
	if len(fake.sets) != 1 {
		t.Fatalf("expected 1 SetRecords call (CNAME), got %d", len(fake.sets))
	}
	if set := fake.appends[0]; set.zone != "fi.gy." {
		t.Errorf("append zone = %q, want fi.gy.", set.zone)
	}

	var appended []libdns.Record
	for _, call := range fake.appends {
		appended = append(appended, call.recs...)
	}
	for _, call := range fake.sets {
		appended = append(appended, call.recs...)
	}
	byKey := make(map[string]libdns.RR, len(appended))
	for _, rec := range appended {
		rr := rec.RR()
		byKey[rr.Name+"/"+rr.Type+"/"+rr.Data] = rr
	}

	otel5 := byKey["otel/A/10.10.0.5"]
	if otel5.TTL != time.Hour {
		t.Errorf("otel ttl = %v, want 1h", otel5.TTL)
	}
	if _, ok := byKey["otel/A/10.10.0.6"]; !ok {
		t.Errorf("missing otel 10.10.0.6; have %v", byKey)
	}
	if obs := byKey["obs/A/10.10.0.7"]; obs.TTL != 5*time.Minute {
		t.Errorf("obs ttl = %v, want 5m (per-record override)", obs.TTL)
	}
	if _, ok := byKey["v6/AAAA/2001:db8::1"]; !ok {
		t.Errorf("missing v6 AAAA; have %v", byKey)
	}
	if _, ok := byKey["gateway/CNAME/otel.fi.gy."]; !ok {
		t.Errorf("missing gateway CNAME; have %v", byKey)
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

func Test_Reconcile_NoChangeSkipsProvider(t *testing.T) {
	// The live zone already holds exactly the declared values, so a reload
	// must not touch the provider at all.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"records": [
				{"name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5", "10.10.0.6"]},
				{"name": "gateway.fi.gy", "type": "CNAME", "value": ["otel.fi.gy."]}
			]
		}],
		"ttl": "1h"
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fake := app.Providers[0].getter.(*fakeProvider)
	fake.Live = []libdns.Record{
		libdns.Address{Name: "otel", TTL: time.Hour, IP: netip.MustParseAddr("10.10.0.5")},
		libdns.Address{Name: "otel", TTL: time.Hour, IP: netip.MustParseAddr("10.10.0.6")},
		libdns.CNAME{Name: "gateway", TTL: time.Hour, Target: "otel.fi.gy."},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.sets) != 0 || len(fake.appends) != 0 || len(fake.deletes) != 0 {
		t.Fatalf("unchanged reconcile made provider calls: sets=%d appends=%d deletes=%d",
			len(fake.sets), len(fake.appends), len(fake.deletes))
	}
}

func Test_Reconcile_InPlaceChangeAppendsBeforeDelete(t *testing.T) {
	// Changing an A value must append the new value before deleting the old
	// one, so that a resolver never observes an empty RRset.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"records": [{"name": "otel.fi.gy", "type": "A", "value": ["10.10.0.6"]}]
		}],
		"ttl": "1h"
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fake := app.Providers[0].getter.(*fakeProvider)
	fake.Live = []libdns.Record{
		libdns.Address{Name: "otel", TTL: time.Hour, IP: netip.MustParseAddr("10.10.0.5")},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.sets) != 0 {
		t.Fatalf("expected no SetRecords, got %d", len(fake.sets))
	}
	if len(fake.appends) != 1 {
		t.Fatalf("expected 1 append, got %d", len(fake.appends))
	}
	if len(fake.deletes) != 1 {
		t.Fatalf("expected 1 delete, got %d", len(fake.deletes))
	}
	want := []string{"append", "delete"}
	if len(fake.ops) != 2 || fake.ops[0] != want[0] || fake.ops[1] != want[1] {
		t.Fatalf("operation order = %v, want %v", fake.ops, want)
	}
	got := fake.appends[0].recs[0].RR()
	if got.Data != "10.10.0.6" {
		t.Errorf("appended %q, want 10.10.0.6", got.Data)
	}
	removed := fake.deletes[0].recs[0].RR()
	if removed.Data != "10.10.0.5" {
		t.Errorf("deleted %q, want 10.10.0.5", removed.Data)
	}
}

func Test_Reconcile_CNAMEChangeUsesSetRecords(t *testing.T) {
	// A CNAME can hold only one target, so it is replaced through SetRecords
	// rather than a fragile delete-then-append pair.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"records": [{"name": "gw.fi.gy", "type": "CNAME", "value": ["new.fi.gy."]}]
		}],
		"ttl": "1h"
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fake := app.Providers[0].getter.(*fakeProvider)
	fake.Live = []libdns.Record{
		libdns.CNAME{Name: "gw", TTL: time.Hour, Target: "old.fi.gy."},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.sets) != 1 {
		t.Fatalf("expected 1 SetRecords for CNAME, got %d", len(fake.sets))
	}
	if len(fake.appends) != 0 || len(fake.deletes) != 0 {
		t.Fatalf("CNAME replace should not append/delete: appends=%d deletes=%d", len(fake.appends), len(fake.deletes))
	}
	if got := fake.sets[0].recs[0].RR().Data; got != "new.fi.gy." {
		t.Errorf("CNAME target = %q, want new.fi.gy.", got)
	}
}

func Test_Reconcile_TTLChangeAppendsWithoutDelete(t *testing.T) {
	// A TTL-only change appends the desired value to refresh it, but must not
	// delete the same value, which would briefly empty the RRset.
	app, err := provisionWith(t, `{
		"providers": [{
			"dns_provider": {"name": "test_fake", "zones": ["fi.gy."]},
			"records": [{"name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5"]}]
		}],
		"ttl": "30m"
	}`)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fake := app.Providers[0].getter.(*fakeProvider)
	fake.Live = []libdns.Record{
		libdns.Address{Name: "otel", TTL: time.Hour, IP: netip.MustParseAddr("10.10.0.5")},
	}
	if err := app.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.appends) != 1 {
		t.Fatalf("expected 1 append for changed TTL, got %d", len(fake.appends))
	}
	if len(fake.deletes) != 0 {
		t.Fatalf("changed TTL must not delete the still-declared value, got %d deletes", len(fake.deletes))
	}
	if ttl := fake.appends[0].recs[0].RR().TTL; ttl != 30*time.Minute {
		t.Errorf("appended ttl = %v, want 30m", ttl)
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
