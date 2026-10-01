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
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/libdns/libdns"
)

// Record and removal types supported by this module.
const (
	typeA     = "A"
	typeAAAA  = "AAAA"
	typeCNAME = "CNAME"
)

// validate checks a RecordSpec's type, name, and values.
func (r RecordSpec) validate() error {
	if r.Name == "" {
		return errors.New("record name is required")
	}
	switch strings.ToUpper(r.Type) {
	case typeA, typeAAAA:
		if len(r.Value) == 0 {
			return fmt.Errorf("%s record %q: at least one value is required", r.Type, r.Name)
		}
		for _, v := range r.Value {
			addr, err := netip.ParseAddr(v)
			if err != nil {
				return fmt.Errorf("%s record %q: invalid IP %q: %v", r.Type, r.Name, v, err)
			}
			if strings.EqualFold(r.Type, typeA) && !addr.Is4() {
				return fmt.Errorf("A record %q: %q is not an IPv4 address", r.Name, v)
			}
			if strings.EqualFold(r.Type, typeAAAA) && !addr.Is6() {
				return fmt.Errorf("AAAA record %q: %q is not an IPv6 address", r.Name, v)
			}
		}
	case typeCNAME:
		if len(r.Value) != 1 {
			return fmt.Errorf("CNAME record %q: exactly one target is required, got %d", r.Name, len(r.Value))
		}
		if _, err := netip.ParseAddr(r.Value[0]); err == nil {
			return fmt.Errorf("CNAME record %q: target %q must be a hostname, not an IP address", r.Name, r.Value[0])
		}
		if r.Value[0] == "" {
			return fmt.Errorf("CNAME record %q: target is required", r.Name)
		}
	default:
		return fmt.Errorf("record %q: unsupported type %q (supported: A, AAAA, CNAME)", r.Name, r.Type)
	}
	return nil
}

// libdnsRecords converts a RecordSpec into one or more libdns records, applying
// TTL precedence: record TTL, then default TTL, then 0.
func (r RecordSpec) libdnsRecords(defaultTTL time.Duration) ([]libdns.Record, error) {
	ttl := time.Duration(r.TTL)
	if ttl == 0 {
		ttl = defaultTTL
	}
	rel := libdns.RelativeName(r.Name, r.Zone)

	switch strings.ToUpper(r.Type) {
	case typeA, typeAAAA:
		recs := make([]libdns.Record, 0, len(r.Value))
		for _, v := range r.Value {
			addr, err := netip.ParseAddr(v)
			if err != nil {
				return nil, fmt.Errorf("record %q: invalid IP %q: %v", r.Name, v, err)
			}
			recs = append(recs, libdns.Address{Name: rel, TTL: ttl, IP: addr})
		}
		return recs, nil
	case typeCNAME:
		return []libdns.Record{libdns.CNAME{Name: rel, TTL: ttl, Target: r.Value[0]}}, nil
	default:
		return nil, fmt.Errorf("record %q: unsupported type %q", r.Name, r.Type)
	}
}

// validate checks a RemoveSpec's type, name, and optional values.
func (r RemoveSpec) validate() error {
	if r.Name == "" {
		return errors.New("remove name is required")
	}
	switch strings.ToUpper(r.Type) {
	case typeA, typeAAAA:
		for _, v := range r.Value {
			addr, err := netip.ParseAddr(v)
			if err != nil {
				return fmt.Errorf("%s remove %q: invalid IP %q: %v", r.Type, r.Name, v, err)
			}
			if strings.EqualFold(r.Type, typeA) && !addr.Is4() {
				return fmt.Errorf("A remove %q: %q is not an IPv4 address", r.Name, v)
			}
			if strings.EqualFold(r.Type, typeAAAA) && !addr.Is6() {
				return fmt.Errorf("AAAA remove %q: %q is not an IPv6 address", r.Name, v)
			}
		}
	case typeCNAME:
		if len(r.Value) > 1 {
			return fmt.Errorf("CNAME remove %q: at most one target may be given, got %d", r.Name, len(r.Value))
		}
	default:
		return fmt.Errorf("remove %q: unsupported type %q (supported: A, AAAA, CNAME)", r.Name, r.Type)
	}
	return nil
}

// canonicalZone returns a zone name with a trailing dot and no surrounding
// whitespace, so it can be compared and passed to libdns.
func canonicalZone(zone string) string {
	return strings.TrimSuffix(strings.TrimSpace(zone), ".") + "."
}

// longestZone returns the longest zone from zones that is a suffix of name.
// Names and zones are compared case-insensitively with trailing dots removed.
func longestZone(zones []libdns.Zone, name string) (string, bool) {
	abs := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	var best string
	for _, z := range zones {
		zn := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(z.Name)), ".")
		if zn == "" {
			continue
		}
		if abs == zn || strings.HasSuffix(abs, "."+zn) {
			if len(zn) > len(best) {
				best = zn
			}
		}
	}
	if best == "" {
		return "", false
	}
	return best + ".", true
}
