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

// Package dnsrec implements the "dns_records" Caddy app module: a static,
// provider-agnostic declaration of A, AAAA, and CNAME records across any
// dns.providers.* modules compiled into the Caddy binary.
//
// Records are reconciled once at Provision/Start and again on every reload.
// Declared (name, type) RRsets are owned and replaced with exactly the declared
// values; records removed from config persist unless an explicit "remove"
// directive is configured.
package dnsrec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/libdns/libdns"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(App{})
}

// App is a Caddy app that declares static DNS records for one or more
// libdns-compatible DNS providers.
type App struct {
	// The configuration for the DNS provider with which the DNS records
	// will be reconciled. If set, it is treated as a single entry in
	// Providers, together with the top-level Records.
	DNSProviderRaw json.RawMessage `json:"dns_provider,omitempty" caddy:"namespace=dns.providers inline_key=name"`

	// Records managed by the legacy single-provider config. When using
	// multiple providers, configure records per entry in Providers instead.
	Records []RecordSpec `json:"records,omitempty"`

	// The list of DNS providers, each managing its own set of records.
	// This allows reconciling records across multiple DNS providers, or
	// multiple accounts of the same provider.
	Providers []Provider `json:"providers,omitempty"`

	// Explicit record removals for the legacy single-provider config.
	Removals []RemoveSpec `json:"remove,omitempty"`

	// Default zone applied to records and removals that do not specify their
	// own zone, for the legacy single-provider config. Optional; when empty,
	// the zone is derived from the provider's ZoneLister.
	Zone string `json:"zone,omitempty"`

	// The TTL to apply to records that do not specify their own.
	// Default: 0 (leave to the provider / zone default).
	TTL caddy.Duration `json:"ttl,omitempty"`

	// If enabled, reconcile failures are logged instead of failing the
	// config load/reload.
	BestEffort bool `json:"best_effort,omitempty"`

	ctx    caddy.Context
	logger *zap.Logger
}

// Provider associates a DNS provider with the records it manages.
type Provider struct {
	// The configuration for the DNS provider with which the DNS records
	// will be reconciled.
	DNSProviderRaw json.RawMessage `json:"dns_provider,omitempty" caddy:"namespace=dns.providers inline_key=name"`

	// Records to create or update.
	Records []RecordSpec `json:"records,omitempty"`

	// Explicit removals; applied on every reconcile.
	Removals []RemoveSpec `json:"remove,omitempty"`

	// Zone applied to this provider's records and removals that do not specify
	// their own zone. Optional; when empty, the zone is derived from the
	// provider's ZoneLister.
	Zone string `json:"zone,omitempty"`

	dnsProvider libdns.RecordSetter
	getter      libdns.RecordGetter
	deleter     libdns.RecordDeleter
}

// RecordSpec is a single declaration of one or more A/AAAA/CNAME values at a name.
type RecordSpec struct {
	// FQDN of the record (fully qualified; not relative to the zone).
	Name string `json:"name,omitempty"`

	// "A", "AAAA", or "CNAME".
	Type string `json:"type,omitempty"`

	// Zone the record belongs to (e.g. "fi.gy"). Optional; if empty, it is
	// derived at Provision from Name by longest-suffix match against the
	// provider's ZoneLister.
	Zone string `json:"zone,omitempty"`

	// A/AAAA: one or more IPs; CNAME: exactly one hostname.
	Value []string `json:"value,omitempty"`

	// Optional per-record TTL override.
	TTL caddy.Duration `json:"ttl,omitempty"`
}

// RemoveSpec is an explicit removal of a record or RRset.
type RemoveSpec struct {
	Name  string   `json:"name,omitempty"`
	Type  string   `json:"type,omitempty"`
	Zone  string   `json:"zone,omitempty"`
	Value []string `json:"value,omitempty"` // empty => remove whole (name,type) RRset
}

// CaddyModule returns the Caddy module information.
func (App) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "dns_records",
		New: func() caddy.Module { return new(App) },
	}
}

// Provision sets up the app module.
func (a *App) Provision(ctx caddy.Context) error {
	a.ctx = ctx
	a.logger = ctx.Logger(a)

	if err := a.normalizeProviders(); err != nil {
		return err
	}

	for i := range a.Providers {
		p := &a.Providers[i]
		if len(p.DNSProviderRaw) == 0 {
			return fmt.Errorf("provider %d: a DNS provider is required", i)
		}
		val, err := ctx.LoadModule(p, "DNSProviderRaw")
		if err != nil {
			return fmt.Errorf("provider %d: loading DNS provider module: %v", i, err)
		}
		setter, ok := val.(libdns.RecordSetter)
		if !ok {
			return fmt.Errorf("provider %d: %T does not implement libdns.RecordSetter", i, val)
		}
		p.dnsProvider = setter

		if getter, ok := val.(libdns.RecordGetter); ok {
			p.getter = getter
		}
		if deleter, ok := val.(libdns.RecordDeleter); ok {
			p.deleter = deleter
		}

		zones, hasLister := a.listZones(val, i)

		for j := range p.Records {
			rec := &p.Records[j]
			if err := rec.validate(); err != nil {
				return fmt.Errorf("provider %d: record %d: %w", i, j, err)
			}
			if rec.Zone == "" {
				rec.Zone = p.Zone
			}
			if err := a.resolveZone(rec.Name, &rec.Zone, zones, hasLister, i); err != nil {
				return err
			}
		}
		for j := range p.Removals {
			rem := &p.Removals[j]
			if err := rem.validate(); err != nil {
				return fmt.Errorf("provider %d: remove %d: %w", i, j, err)
			}
			if p.deleter == nil {
				return fmt.Errorf("provider %d: configured with remove directives, but its module does not implement libdns.RecordDeleter", i)
			}
			if p.getter == nil {
				return fmt.Errorf("provider %d: configured with remove directives, but its module does not implement libdns.RecordGetter", i)
			}
			if rem.Zone == "" {
				rem.Zone = p.Zone
			}
			if err := a.resolveZone(rem.Name, &rem.Zone, zones, hasLister, i); err != nil {
				return err
			}
		}
	}

	return nil
}

// listZones returns the provider's zones if it implements libdns.ZoneLister.
func (a *App) listZones(provider any, idx int) ([]libdns.Zone, bool) {
	lister, ok := provider.(libdns.ZoneLister)
	if !ok {
		return nil, false
	}
	zones, err := lister.ListZones(a.ctx)
	if err != nil {
		a.logger.Warn("could not list zones for provider; an explicit zone will be required",
			zap.Int("provider", idx),
			zap.Error(err))
		return nil, false
	}
	return zones, true
}

// resolveZone fills *zone when empty, using the provider's zone list to find
// the longest suffix matching name. It errors when no zone can be determined.
func (a *App) resolveZone(name string, zone *string, zones []libdns.Zone, hasLister bool, idx int) error {
	if *zone != "" {
		*zone = canonicalZone(*zone)
		return nil
	}
	if !hasLister {
		return fmt.Errorf("provider %d: record %q has no zone and the provider does not implement libdns.ZoneLister; set \"zone\" explicitly", idx, name)
	}
	z, ok := longestZone(zones, name)
	if !ok {
		return fmt.Errorf("provider %d: could not derive a zone for %q from the provider's zones; set \"zone\" explicitly", idx, name)
	}
	*zone = z
	return nil
}

// Start performs one reconcile pass. It returns after reconciling; there is no
// background loop.
func (a *App) Start() error {
	err := a.reconcile()
	if err != nil {
		if a.BestEffort {
			a.logger.Error("dns_records reconcile failed (best_effort)", zap.Error(err))
			return nil
		}
		return err
	}
	return nil
}

// Stop is a no-op.
func (a *App) Stop() error {
	return nil
}

// reconcile applies the declared records and removals for every provider.
func (a *App) reconcile() error {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()

	for i := range a.Providers {
		p := &a.Providers[i]

		byZone := make(map[string][]libdns.Record)
		for _, rec := range p.Records {
			recs, err := rec.libdnsRecords(time.Duration(a.TTL))
			if err != nil {
				return err
			}
			byZone[rec.Zone] = append(byZone[rec.Zone], recs...)
		}
		for zone, recs := range byZone {
			set, err := p.dnsProvider.SetRecords(ctx, zone, recs)
			if err != nil {
				return fmt.Errorf("provider %d: setting records in zone %s: %w", i, zone, err)
			}
			a.logger.Info("set DNS records",
				zap.Int("provider", i),
				zap.String("zone", zone),
				zap.Int("count", len(set)))
		}

		removalsByZone := make(map[string][]RemoveSpec)
		for _, rem := range p.Removals {
			removalsByZone[rem.Zone] = append(removalsByZone[rem.Zone], rem)
		}
		for zone, rems := range removalsByZone {
			deleted, err := a.deleteResolved(ctx, p, zone, rems)
			if err != nil {
				return fmt.Errorf("provider %d: removing records in zone %s: %w", i, zone, err)
			}
			a.logger.Info("removed DNS records",
				zap.Int("provider", i),
				zap.String("zone", zone),
				zap.Int("count", len(deleted)))
		}
	}

	return nil
}

// deleteResolved deletes the specified records from a zone. It fetches the
// zone's live records and matches each removal against them, so that deletion
// is independent of TTL mismatches and so that a whole-RRset removal (no value
// given) works even with providers that require exact value matches.
func (a *App) deleteResolved(ctx context.Context, p *Provider, zone string, rems []RemoveSpec) ([]libdns.Record, error) {
	records, err := p.getter.GetRecords(ctx, zone)
	if err != nil {
		return nil, err
	}

	var toDelete []libdns.Record
	for _, rem := range rems {
		name := libdns.RelativeName(rem.Name, zone)
		typ := strings.ToUpper(rem.Type)
		want := make(map[string]bool, len(rem.Value))
		for _, v := range rem.Value {
			want[v] = true
		}
		for _, rec := range records {
			rr := rec.RR()
			if rr.Name != name || rr.Type != typ {
				continue
			}
			if len(rem.Value) == 0 || want[rr.Data] {
				toDelete = append(toDelete, rec)
			}
		}
	}
	if len(toDelete) == 0 {
		return nil, nil
	}
	return p.deleter.DeleteRecords(ctx, zone, toDelete)
}

// normalizeProviders folds the legacy single-provider config into the
// providers list and validates the result.
func (a *App) normalizeProviders() error {
	if len(a.DNSProviderRaw) > 0 {
		a.Providers = append([]Provider{{
			DNSProviderRaw: a.DNSProviderRaw,
			Records:        a.Records,
			Removals:       a.Removals,
			Zone:           a.Zone,
		}}, a.Providers...)
		a.DNSProviderRaw = nil
		a.Records = nil
		a.Removals = nil
		a.Zone = ""
	} else if len(a.Records) > 0 || len(a.Removals) > 0 || a.Zone != "" {
		// top-level records/zone without a top-level provider can only belong
		// to a sole provider that has no records of its own
		if len(a.Providers) == 1 && a.Providers[0].Records == nil && a.Providers[0].Removals == nil {
			a.Providers[0].Records = a.Records
			a.Providers[0].Removals = a.Removals
			if a.Providers[0].Zone == "" {
				a.Providers[0].Zone = a.Zone
			}
			a.Records = nil
			a.Removals = nil
			a.Zone = ""
		} else {
			return fmt.Errorf("with multiple providers, records, remove and zone must be configured per provider")
		}
	}
	if len(a.Providers) == 0 {
		return fmt.Errorf("a DNS provider is required")
	}
	return nil
}

// Interface guards
var (
	_ caddy.Provisioner = (*App)(nil)
	_ caddy.App         = (*App)(nil)
)
