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
	"errors"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func init() {
	httpcaddyfile.RegisterGlobalOption("dns_records", parseApp)
}

// parseApp configures the "dns_records" global option from a Caddyfile.
// Syntax:
//
//	dns_records {
//		provider <name> [<args...>] {
//			record <name> <type> <value...> [ttl <duration>]
//			remove <name> <type> [<value...>]
//			zone <zone>
//			# ...and any tokens belonging to the DNS provider module
//		}
//		record <name> <type> <value...> [ttl <duration>]
//		remove <name> <type> [<value...>]
//		zone <zone>
//		ttl <duration>
//		best_effort
//	}
//
// The provider directive may be repeated to manage records across multiple
// DNS providers, or multiple accounts of the same provider. Inside a provider
// block, the names "record", "remove", and "zone" are reserved for this module;
// every other token in the block belongs to the DNS provider module as usual.
//
// Record and remove directives at the top level (outside any provider block)
// are the legacy single-provider shorthand; they are folded into the sole
// provider. The zone is derived from the record name via the provider's
// libdns.ZoneLister when it is omitted and no zone directive is set.
func parseApp(d *caddyfile.Dispenser, _ any) (any, error) {
	app := new(App)

	// consume the option name
	if !d.Next() {
		return nil, d.ArgErr()
	}

	for d.NextBlock(0) {
		switch d.Val() {
		case "provider":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			provName := d.Val()
			modID := "dns.providers." + provName

			var prov Provider
			moduleTokens, err := splitProviderSegment(d.NextSegment(), &prov)
			if err != nil {
				return nil, err
			}
			unm, err := unmarshalModuleTokens(d, modID, moduleTokens)
			if err != nil {
				return nil, err
			}
			prov.DNSProviderRaw = caddyconfig.JSONModuleObject(unm, "name", provName, nil)
			app.Providers = append(app.Providers, prov)

		case "record":
			rec, err := parseRecord(d)
			if err != nil {
				return nil, err
			}
			app.Records = append(app.Records, rec)

		case "remove":
			rem, err := parseRemove(d)
			if err != nil {
				return nil, err
			}
			app.Removals = append(app.Removals, rem)

		case "zone":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			app.Zone = d.Val()

		case "ttl":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			dur, err := caddy.ParseDuration(d.Val())
			if err != nil {
				return nil, err
			}
			app.TTL = caddy.Duration(dur)

		case "best_effort":
			if d.NextArg() {
				return nil, d.ArgErr()
			}
			app.BestEffort = true

		default:
			return nil, d.ArgErr()
		}
	}

	return httpcaddyfile.App{
		Name:  "dns_records",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}

// parseRecord parses a record directive. The dispenser must be positioned on
// the "record" token.
//
//	record <name> <type> <value...> [ttl <duration>]
func parseRecord(d *caddyfile.Dispenser) (RecordSpec, error) {
	var rec RecordSpec
	if !d.NextArg() {
		return rec, d.ArgErr()
	}
	rec.Name = d.Val()
	if !d.NextArg() {
		return rec, d.ArgErr()
	}
	rec.Type = strings.ToUpper(d.Val())
	values, ttl, err := splitValuesTTL(d.RemainingArgs())
	if err != nil {
		return rec, d.Errf("record %q: %v", rec.Name, err)
	}
	rec.Value = values
	rec.TTL = ttl
	return rec, nil
}

// parseRemove parses a remove directive. The dispenser must be positioned on
// the "remove" token.
//
//	remove <name> <type> [<value...>]
func parseRemove(d *caddyfile.Dispenser) (RemoveSpec, error) {
	var rem RemoveSpec
	if !d.NextArg() {
		return rem, d.ArgErr()
	}
	rem.Name = d.Val()
	if !d.NextArg() {
		return rem, d.ArgErr()
	}
	rem.Type = strings.ToUpper(d.Val())
	rem.Value = d.RemainingArgs()
	return rem, nil
}

// splitValuesTTL separates a record's value arguments from an optional trailing
// "ttl <duration>".
func splitValuesTTL(args []string) ([]string, caddy.Duration, error) {
	// "ttl" must be followed by a duration, so the shortest valid form is
	// "<value> ttl <duration>".
	if n := len(args); n >= 2 && args[n-2] == "ttl" {
		dur, err := caddy.ParseDuration(args[n-1])
		if err != nil {
			return nil, 0, err
		}
		return args[:n-2], caddy.Duration(dur), nil
	}
	if len(args) >= 1 && args[len(args)-1] == "ttl" {
		return nil, 0, errors.New("ttl requires a duration")
	}
	return args, 0, nil
}

// splitProviderSegment partitions the tokens of a provider directive's segment
// (the provider module name through the end of its block) into the tokens
// destined for the DNS provider module and the record/remove blocks reserved by
// this app, which are parsed into prov. The returned tokens are what remains for
// the DNS provider module to unmarshal.
func splitProviderSegment(seg caddyfile.Segment, prov *Provider) ([]caddyfile.Token, error) {
	d := caddyfile.NewDispenser(seg)

	// the provider module name and its inline arguments
	d.Next()
	moduleTokens := []caddyfile.Token{d.Token()}
	for d.NextArg() {
		moduleTokens = append(moduleTokens, d.Token())
	}

	var blockTokens []caddyfile.Token
	var openBrace, closeBrace caddyfile.Token
	inBlock := false
	for nesting := d.Nesting(); d.NextBlock(nesting); {
		if !inBlock {
			// NextBlock() consumed the open curly brace; rewind to
			// capture it, so that the module's block can be rebuilt
			// with its surrounding braces
			d.Prev()
			openBrace = d.Token()
			d.Next()
			inBlock = true
		}
		switch d.Val() {
		case "record":
			rec, err := parseRecord(d)
			if err != nil {
				return nil, err
			}
			prov.Records = append(prov.Records, rec)
		case "remove":
			rem, err := parseRemove(d)
			if err != nil {
				return nil, err
			}
			prov.Removals = append(prov.Removals, rem)
		case "zone":
			if !d.NextArg() {
				return nil, d.ArgErr()
			}
			prov.Zone = d.Val()
		default:
			// everything else belongs to the DNS provider module
			blockTokens = append(blockTokens, d.NextSegment()...)
		}
	}
	if inBlock {
		closeBrace = d.Token()
		// if the block contained only record/remove directives, leave the
		// braces off so the module sees the same tokens as without a block
		if len(blockTokens) > 0 {
			moduleTokens = append(moduleTokens, openBrace)
			moduleTokens = append(moduleTokens, blockTokens...)
			moduleTokens = append(moduleTokens, closeBrace)
		}
	}
	return moduleTokens, nil
}

// unmarshalModuleTokens is like caddyfile.UnmarshalModule, except that it
// unmarshals the module from an explicit list of tokens instead of from the
// dispenser's next segment.
func unmarshalModuleTokens(d *caddyfile.Dispenser, moduleID string, tokens []caddyfile.Token) (caddyfile.Unmarshaler, error) {
	mod, err := caddy.GetModule(moduleID)
	if err != nil {
		return nil, d.Errf("getting module named '%s': %v", moduleID, err)
	}
	inst := mod.New()
	unm, ok := inst.(caddyfile.Unmarshaler)
	if !ok {
		return nil, d.Errf("module %s is not a Caddyfile unmarshaler; is %T", mod.ID, inst)
	}
	err = unm.UnmarshalCaddyfile(caddyfile.NewDispenser(tokens))
	if err != nil {
		return nil, err
	}
	return unm, nil
}
