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
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
)

func init() {
	caddy.RegisterModule(dummyProvider{})
}

// dummyProvider is a stand-in DNS provider module so tests can exercise the
// provider directive without depending on a real DNS provider. Like real
// providers it accepts inline arguments or a config block, and it errors on
// unrecognized subdirectives, which proves tokens reserved by this app never
// leak through to the provider module.
type dummyProvider struct {
	Arg    string `json:"arg,omitempty"`
	APIKey string `json:"api_key,omitempty"`
}

func (dummyProvider) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "dns.providers.test_dummy",
		New: func() caddy.Module { return new(dummyProvider) },
	}
}

func (p *dummyProvider) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			p.Arg = d.Val()
		}
		if d.NextArg() {
			return d.ArgErr()
		}
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			switch d.Val() {
			case "api_key":
				if !d.NextArg() {
					return d.ArgErr()
				}
				p.APIKey = d.Val()
			default:
				return d.Errf("unrecognized subdirective '%s'", d.Val())
			}
		}
	}
	return nil
}

func equivalentJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got invalid JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("parseApp JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func Test_ParseApp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name: "legacy single provider with top-level record",
			input: `
			dns_records {
				provider test_dummy abc123
				record otel.fi.gy A 10.10.0.5
				ttl 1h
			}`,
			want: `{
				"providers": [
					{
						"dns_provider": {"name": "test_dummy", "arg": "abc123"}
					}
				],
				"records": [
					{"name": "otel.fi.gy", "type": "A", "value": ["10.10.0.5"]}
				],
				"ttl": 3600000000000
			}`,
		},
		{
			name: "multi provider with nested records and removals",
			input: `
			dns_records {
				provider test_dummy {
					api_key secret1
					record a.fi.gy A 10.10.0.5
					record b.fi.gy AAAA 2001:db8::1
					record c.fi.gy CNAME a.fi.gy.
					record d.fi.gy A 10.10.0.6 ttl 5m
					remove stale.fi.gy A
					remove stale2.fi.gy A 10.10.0.99
				}
				provider test_dummy abc2 {
					api_key secret2
					record example.com A 203.0.113.10
				}
			}`,
			want: `{
				"providers": [
					{
						"dns_provider": {"name": "test_dummy", "api_key": "secret1"},
						"records": [
							{"name": "a.fi.gy", "type": "A", "value": ["10.10.0.5"]},
							{"name": "b.fi.gy", "type": "AAAA", "value": ["2001:db8::1"]},
							{"name": "c.fi.gy", "type": "CNAME", "value": ["a.fi.gy."]},
							{"name": "d.fi.gy", "type": "A", "value": ["10.10.0.6"], "ttl": 300000000000}
						],
						"remove": [
							{"name": "stale.fi.gy", "type": "A"},
							{"name": "stale2.fi.gy", "type": "A", "value": ["10.10.0.99"]}
						]
					},
					{
						"dns_provider": {"name": "test_dummy", "arg": "abc2", "api_key": "secret2"},
						"records": [
							{"name": "example.com", "type": "A", "value": ["203.0.113.10"]}
						]
					}
				]
			}`,
		},
		{
			name: "best_effort",
			input: `
			dns_records {
				provider test_dummy
				record a.fi.gy A 10.10.0.5
				best_effort
			}`,
			want: `{
				"providers": [{"dns_provider": {"name": "test_dummy"}}],
				"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.10.0.5"]}],
				"best_effort": true
			}`,
		},
		{
			name: "top-level zone folds into sole provider",
			input: `
			dns_records {
				provider test_dummy
				zone fi.gy.
				record a.fi.gy A 10.10.0.5
			}`,
			want: `{
				"providers": [{"dns_provider": {"name": "test_dummy"}}],
				"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.10.0.5"]}],
				"zone": "fi.gy."
			}`,
		},
		{
			name: "per-provider zone with other provider tokens",
			input: `
			dns_records {
				provider test_dummy {
					api_key secret1
					zone fi.gy.
					record a.fi.gy A 10.10.0.5
				}
			}`,
			want: `{
				"providers": [{
					"dns_provider": {"name": "test_dummy", "api_key": "secret1"},
					"records": [{"name": "a.fi.gy", "type": "A", "value": ["10.10.0.5"]}],
					"zone": "fi.gy."
				}]
			}`,
		},
		{
			name: "unrecognized top-level directive",
			input: `
			dns_records {
				bogus_directive
			}`,
			wantErr: true,
		},
		{
			name: "unrecognized provider subdirective leaks through and errors",
			input: `
			dns_records {
				provider test_dummy {
					bogus
				}
			}`,
			wantErr: true,
		},
		{
			name: "record with trailing ttl but no duration",
			input: `
			dns_records {
				provider test_dummy
				record a.fi.gy A 10.10.0.5 ttl
			}`,
			wantErr: true,
		},
		{
			name: "record missing value",
			input: `
			dns_records {
				provider test_dummy
				record a.fi.gy A
			}`,
			want: `{
				"providers": [{"dns_provider": {"name": "test_dummy"}}],
				"records": [{"name": "a.fi.gy", "type": "A"}]
			}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseApp(caddyfile.NewTestDispenser(tt.input), nil)
			if err != nil {
				if !tt.wantErr {
					t.Errorf("parseApp() error = %v, wantErr %v", err, tt.wantErr)
				}
				return
			}
			if tt.wantErr {
				t.Fatalf("parseApp() expected error, got none: %v", got)
			}
			equivalentJSON(t, string(got.(httpcaddyfile.App).Value), tt.want)
		})
	}
}
