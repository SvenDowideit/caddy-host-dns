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

	// Optional SOA management for the legacy single-provider config. When
	// unset (the default), the zone SOA is left untouched.
	SOA *SOASpec `json:"soa,omitempty"`

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

	// Optional SOA management for this provider's zone. When unset (the
	// default), the zone SOA is left untouched.
	SOA *SOASpec `json:"soa,omitempty"`

	// Zone applied to this provider's records and removals that do not specify
	// their own zone. Optional; when empty, the zone is derived from the
	// provider's ZoneLister.
	Zone string `json:"zone,omitempty"`

	dnsProvider libdns.RecordSetter
	appender    libdns.RecordAppender
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

		if appender, ok := val.(libdns.RecordAppender); ok {
			p.appender = appender
		}
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
		if err := a.resolveSOA(p, i, zones, hasLister); err != nil {
			return err
		}
	}

	return nil
}

// resolveSOA validates the provider's SOA spec and resolves the zone it
// applies to. The zone is taken from the SOA block, then the provider's zone,
// then a single zone shared by the provider's records/removals.
func (a *App) resolveSOA(p *Provider, idx int, zones []libdns.Zone, hasLister bool) error {
	if p.SOA == nil {
		return nil
	}
	if err := p.SOA.validate(); err != nil {
		return fmt.Errorf("provider %d: soa: %w", idx, err)
	}
	if p.SOA.Zone == "" {
		p.SOA.Zone = p.Zone
	}
	switch {
	case p.SOA.Zone != "":
		p.SOA.Zone = canonicalZone(p.SOA.Zone)
	case sharedZone(p) != "":
		p.SOA.Zone = sharedZone(p)
	case hasLister && len(zones) == 1:
		p.SOA.Zone = canonicalZone(zones[0].Name)
	case hasLister:
		return fmt.Errorf("provider %d: soa: a zone is required (set \"zone\" in the soa block or on the provider)", idx)
	default:
		return fmt.Errorf("provider %d: soa: a zone is required; the provider does not implement libdns.ZoneLister", idx)
	}
	if p.getter == nil && (p.SOA.MName == "" || p.SOA.RName == "") {
		return fmt.Errorf("provider %d: soa: mname and rname are required when the provider does not implement libdns.RecordGetter", idx)
	}
	return nil
}

// sharedZone returns the single zone used by every record and removal of the
// provider, or "" when there is none or more than one.
func sharedZone(p *Provider) string {
	var z string
	for _, rec := range p.Records {
		if z == "" {
			z = rec.Zone
		} else if z != rec.Zone {
			return ""
		}
	}
	for _, rem := range p.Removals {
		if z == "" {
			z = rem.Zone
		} else if z != rem.Zone {
			return ""
		}
	}
	return z
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

		desiredByZone := make(map[string][]libdns.Record)
		for _, rec := range p.Records {
			recs, err := rec.libdnsRecords(time.Duration(a.TTL))
			if err != nil {
				return err
			}
			desiredByZone[rec.Zone] = append(desiredByZone[rec.Zone], recs...)
		}

		removalsByZone := make(map[string][]RemoveSpec)
		for _, rem := range p.Removals {
			removalsByZone[rem.Zone] = append(removalsByZone[rem.Zone], rem)
		}

		zones := make(map[string]struct{}, len(desiredByZone)+len(removalsByZone))
		for zone := range desiredByZone {
			zones[zone] = struct{}{}
		}
		for zone := range removalsByZone {
			zones[zone] = struct{}{}
		}
		soaZone := ""
		if p.SOA != nil {
			soaZone = p.SOA.Zone
			zones[soaZone] = struct{}{}
		}

		for zone := range zones {
			// Fetch the live zone once and reuse it for the record diff,
			// removal resolution, and SOA comparison, so we issue at most one
			// read per zone.
			var live []libdns.Record
			if p.getter != nil && (len(desiredByZone[zone]) > 0 || len(removalsByZone[zone]) > 0 || (p.SOA != nil && soaZone == zone)) {
				var err error
				live, err = p.getter.GetRecords(ctx, zone)
				if err != nil {
					return fmt.Errorf("provider %d: reading records in zone %s: %w", i, zone, err)
				}
			}

			// Write the SOA before the record updates. Some providers (e.g.
			// BIND with its default serial-update-method=increment) bump the
			// SOA serial on every dynamic update, so a serial derived from the
			// pre-reconcile snapshot would be stale—and lower than the
			// provider's current serial—by the time the record updates have
			// run. Writing it first keeps the increment valid.
			if p.SOA != nil && soaZone == zone {
				a.reconcileSOA(ctx, p, i, zone, live)
			}

			if err := a.reconcileRecords(ctx, p, i, zone, desiredByZone[zone], live); err != nil {
				return err
			}

			if rems := removalsByZone[zone]; len(rems) > 0 {
				deleted, err := a.deleteResolved(ctx, p, zone, rems, live)
				if err != nil {
					return fmt.Errorf("provider %d: removing records in zone %s: %w", i, zone, err)
				}
				a.logger.Info("removed DNS records",
					zap.Int("provider", i),
					zap.String("zone", zone),
					zap.Int("count", len(deleted)))
			}
		}
	}

	return nil
}

// reconcileRecords brings the declared records for one zone in line with the
// live zone. It first checks whether the declared (name, type, value) RRsets
// already match, in which case it does nothing. Otherwise, when the provider
// supports append + delete, it updates in place by appending new values before
// deleting stale ones, so an RRset is never momentarily empty (which can be
// negatively cached by resolvers). Providers that cannot append/delete
// individually fall back to SetRecords.
func (a *App) reconcileRecords(ctx context.Context, p *Provider, i int, zone string, desired, live []libdns.Record) error {
	if len(desired) == 0 {
		return nil
	}

	// Without a getter we cannot compare against the live zone; use the
	// provider's SetRecords, which owns each declared RRset.
	if p.getter == nil {
		set, err := p.dnsProvider.SetRecords(ctx, zone, desired)
		if err != nil {
			return fmt.Errorf("provider %d: setting records in zone %s: %w", i, zone, err)
		}
		a.logger.Info("set DNS records",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Int("count", len(set)))
		return nil
	}

	// Guard: if every declared RRset already holds exactly the declared
	// values, leave the zone untouched. This is what prevents a reload from
	// needlessly delete/re-adding an unchanged record.
	if rrsetsMatch(desired, live) {
		a.logger.Debug("DNS records already up to date",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Int("count", len(desired)))
		return nil
	}

	if p.appender != nil && p.deleter != nil {
		return a.reconcileInPlace(ctx, p, i, zone, desired, live)
	}

	// Cannot append/delete individually: fall back to SetRecords.
	set, err := p.dnsProvider.SetRecords(ctx, zone, desired)
	if err != nil {
		return fmt.Errorf("provider %d: setting records in zone %s: %w", i, zone, err)
	}
	a.logger.Info("set DNS records",
		zap.Int("provider", i),
		zap.String("zone", zone),
		zap.Int("count", len(set)))
	return nil
}

// reconcileSOA brings the zone's SOA record in line with the provider's SOA
// spec. SOA management is advisory: many providers manage the SOA themselves
// and reject or silently ignore writes, so any failure here is logged and
// swallowed rather than failing the config load/reload. It does nothing when
// the live SOA already matches, so a reload does not churn the serial. The SOA
// is written through SetRecords, which owns only the apex SOA RRset and leaves
// all other records untouched.
func (a *App) reconcileSOA(ctx context.Context, p *Provider, i int, zone string, live []libdns.Record) {
	desired, changed, err := p.SOA.desired(time.Duration(a.TTL), findSOA(live, zone))
	if err != nil {
		a.logger.Warn("skipping SOA: could not build the desired record",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Error(err))
		return
	}
	if !changed {
		a.logger.Debug("DNS SOA already up to date",
			zap.Int("provider", i),
			zap.String("zone", zone))
		return
	}
	set, err := p.dnsProvider.SetRecords(ctx, zone, []libdns.Record{desired})
	if err != nil {
		a.logger.Warn("could not set SOA (the provider may manage it itself); ignoring",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Error(err))
		return
	}
	a.logger.Info("set DNS SOA",
		zap.Int("provider", i),
		zap.String("zone", zone),
		zap.Int("count", len(set)),
		zap.String("data", desired.RR().Data))
}

// reconcileInPlace updates a zone using the provider's AppendRecords and
// DeleteRecords. A/AAAA values are appended before stale values are deleted so
// the RRset never becomes empty (which a resolver could negatively cache).
// CNAMEs cannot legally coexist, so a changed CNAME is replaced via SetRecords.
//
// A value that is still declared but whose TTL changed is refreshed by
// appending it again; it is never deleted, since providers key records by
// value and the append already updates the TTL.
func (a *App) reconcileInPlace(ctx context.Context, p *Provider, i int, zone string, desired, live []libdns.Record) error {
	desiredByKey := groupByRRset(desired)
	liveByKey := groupByRRset(live)

	var appendFirst, deleteAfter, cnameChanged []libdns.Record
	for key, want := range desiredByKey {
		have := liveByKey[key]
		for _, w := range want {
			if containsRecord(have, w) {
				continue
			}
			if key.typ == typeCNAME {
				cnameChanged = append(cnameChanged, w)
				continue
			}
			// New value, or an existing value whose TTL changed: append
			// the desired record in both cases.
			appendFirst = append(appendFirst, w)
		}
		for _, h := range have {
			if containsRecord(want, h) {
				continue
			}
			// Keep a value that is still declared (only its TTL changed);
			// the append above refreshes it.
			if containsRecordIgnoringTTL(want, h) {
				continue
			}
			if key.typ == typeCNAME {
				continue
			}
			deleteAfter = append(deleteAfter, h)
		}
	}

	// Append new A/AAAA values first: the RRset keeps serving the old values
	// (and all still-declared values) until the new ones are in place, then
	// the stale ones are pruned below.
	if len(appendFirst) > 0 {
		appended, err := p.appender.AppendRecords(ctx, zone, appendFirst)
		if err != nil {
			return fmt.Errorf("provider %d: appending records in zone %s: %w", i, zone, err)
		}
		a.logger.Info("appended DNS records",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Int("count", len(appended)))
	}

	if len(deleteAfter) > 0 {
		deleted, err := p.deleter.DeleteRecords(ctx, zone, deleteAfter)
		if err != nil {
			return fmt.Errorf("provider %d: deleting stale records in zone %s: %w", i, zone, err)
		}
		a.logger.Info("deleted stale DNS records",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Int("count", len(deleted)))
	}

	// A CNAME RRset can hold only one target, so there is no non-empty state
	// to preserve across a change. Replace it with the provider's SetRecords,
	// which is a single call and atomic on providers that support it.
	if len(cnameChanged) > 0 {
		set, err := p.dnsProvider.SetRecords(ctx, zone, cnameChanged)
		if err != nil {
			return fmt.Errorf("provider %d: setting CNAME in zone %s: %w", i, zone, err)
		}
		a.logger.Info("set DNS CNAME",
			zap.Int("provider", i),
			zap.String("zone", zone),
			zap.Int("count", len(set)))
	}

	return nil
}

// rrsetKey identifies an RRset by its (relative) name and uppercase type.
type rrsetKey struct {
	name string
	typ  string
}

// groupByRRset groups records by their (name, type) RRset.
func groupByRRset(recs []libdns.Record) map[rrsetKey][]libdns.Record {
	out := make(map[rrsetKey][]libdns.Record)
	for _, rec := range recs {
		rr := rec.RR()
		key := rrsetKey{name: normalizeName(rr.Name), typ: strings.ToUpper(rr.Type)}
		out[key] = append(out[key], rec)
	}
	return out
}

// normalizeName lowercases a relative name and trims a trailing dot so that
// names compare consistently across providers.
func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// recordsEquivalent reports whether a live record already satisfies a desired
// record: same (name, type, value), and, when a non-zero TTL is desired, the
// same TTL. A desired TTL of 0 means "provider default", so the live TTL is
// not compared in that case.
func recordsEquivalent(desired, live libdns.Record) bool {
	d, l := desired.RR(), live.RR()
	if normalizeName(d.Name) != normalizeName(l.Name) {
		return false
	}
	if !strings.EqualFold(d.Type, l.Type) {
		return false
	}
	if d.Data != l.Data {
		return false
	}
	if d.TTL != 0 && d.TTL != l.TTL {
		return false
	}
	return true
}

// containsRecord reports whether list holds a record equivalent to want.
func containsRecord(list []libdns.Record, want libdns.Record) bool {
	for _, rec := range list {
		if recordsEquivalent(rec, want) {
			return true
		}
	}
	return false
}

// containsRecordIgnoringTTL reports whether list holds a record with the same
// (name, type, value) as want, regardless of TTL.
func containsRecordIgnoringTTL(list []libdns.Record, want libdns.Record) bool {
	w := want.RR()
	for _, rec := range list {
		r := rec.RR()
		if normalizeName(r.Name) == normalizeName(w.Name) &&
			strings.EqualFold(r.Type, w.Type) &&
			r.Data == w.Data {
			return true
		}
	}
	return false
}

// rrsetsMatch reports whether every declared RRset already holds exactly the
// declared values in the live zone. RRsets that are not declared are ignored,
// so undeclared names keep persisting as before.
func rrsetsMatch(desired, live []libdns.Record) bool {
	liveByKey := groupByRRset(live)
	for key, want := range groupByRRset(desired) {
		have := liveByKey[key]
		if len(have) != len(want) {
			return false
		}
		for _, w := range want {
			if !containsRecord(have, w) {
				return false
			}
		}
	}
	return true
}

// deleteResolved deletes the specified records from a zone. It matches each
// removal against the supplied live records, so that deletion is independent of
// TTL mismatches and so that a whole-RRset removal (no value given) works even
// with providers that require exact value matches.
func (a *App) deleteResolved(ctx context.Context, p *Provider, zone string, rems []RemoveSpec, records []libdns.Record) ([]libdns.Record, error) {
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
			if normalizeName(rr.Name) != normalizeName(name) || rr.Type != typ {
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
			SOA:            a.SOA,
			Zone:           a.Zone,
		}}, a.Providers...)
		a.DNSProviderRaw = nil
		a.Records = nil
		a.Removals = nil
		a.SOA = nil
		a.Zone = ""
	} else if len(a.Records) > 0 || len(a.Removals) > 0 || a.SOA != nil || a.Zone != "" {
		// top-level records/zone/soa without a top-level provider can only
		// belong to a sole provider that has none of its own
		if len(a.Providers) == 1 && a.Providers[0].Records == nil && a.Providers[0].Removals == nil && a.Providers[0].SOA == nil {
			a.Providers[0].Records = a.Records
			a.Providers[0].Removals = a.Removals
			a.Providers[0].SOA = a.SOA
			if a.Providers[0].Zone == "" {
				a.Providers[0].Zone = a.Zone
			}
			a.Records = nil
			a.Removals = nil
			a.SOA = nil
			a.Zone = ""
		} else {
			return fmt.Errorf("with multiple providers, records, remove, soa and zone must be configured per provider")
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
