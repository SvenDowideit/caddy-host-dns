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
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/libdns/libdns"
)

// soaType is the DNS type of the zone authority record.
const soaType = "SOA"

// Small, dynamic-DNS-friendly SOA timers used for any field the operator does
// not set explicitly. They keep negative caching and secondary polling short
// so that record changes propagate quickly. Providers may enforce a higher
// floor; use MinTTL to match it and avoid rejected writes.
const (
	soaDefaultRefresh = 5 * time.Minute
	soaDefaultRetry   = 5 * time.Minute
	soaDefaultExpire  = 10 * time.Minute
	soaDefaultMinimum = 1 * time.Minute
	soaDefaultTTL     = 1 * time.Minute
)

// SOASpec configures the zone SOA record. When a provider has no SOA block the
// SOA is left untouched; setting one opts that zone into management and
// reconciles the SOA to (usually small) timers.
//
// Only a RecordSetter is required. When the provider also implements
// RecordGetter and returns the SOA, the parts not configured here (MName,
// RName, and the serial) are inherited from the live record, and the serial is
// incremented only when another field actually changes. When the live SOA is
// not available, MName and RName must be set and the serial defaults to the
// current Unix time.
type SOASpec struct {
	// Primary nameserver (MNAME), a domain name. Optional; inherited from the
	// live SOA when omitted.
	MName string `json:"mname,omitempty"`

	// Responsible mailbox (RNAME), a domain name where the first label is the
	// local part with "." instead of "@" (e.g. "hostmaster.example.com.").
	// Optional; inherited from the live SOA when omitted.
	RName string `json:"rname,omitempty"`

	// Serial. Optional; when omitted (0), the live serial is incremented on a
	// change, or the current Unix time is used when there is no live SOA.
	Serial uint32 `json:"serial,omitempty"`

	// Secondary refresh interval. Default: 5m.
	Refresh caddy.Duration `json:"refresh,omitempty"`

	// Secondary retry interval. Default: 5m.
	Retry caddy.Duration `json:"retry,omitempty"`

	// Secondary expiry interval. Default: 10m.
	Expire caddy.Duration `json:"expire,omitempty"`

	// Negative-caching TTL (RFC 2308 MINIMUM). Default: 1m.
	Minimum caddy.Duration `json:"minimum,omitempty"`

	// TTL of the SOA record itself. Default: the app TTL, else 1m.
	TTL caddy.Duration `json:"ttl,omitempty"`

	// Lower bound applied to every configured duration, so that a provider's
	// minimum TTL is never violated. 0 disables the clamp.
	MinTTL caddy.Duration `json:"min_ttl,omitempty"`

	// Zone whose SOA is managed. Optional; defaults to the provider's zone (or
	// a single zone shared by the provider's records).
	Zone string `json:"zone,omitempty"`
}

// soaFields is the parsed RDATA of an SOA record, with durations in seconds.
type soaFields struct {
	mname, rname                            string
	serial, refresh, retry, expire, minimum uint32
}

// validate checks the SOA timers and names.
func (s *SOASpec) validate() error {
	for _, f := range []struct {
		name string
		v    caddy.Duration
	}{
		{"refresh", s.Refresh},
		{"retry", s.Retry},
		{"expire", s.Expire},
		{"minimum", s.Minimum},
		{"ttl", s.TTL},
		{"min_ttl", s.MinTTL},
	} {
		if f.v < 0 {
			return fmt.Errorf("%s must not be negative", f.name)
		}
	}
	if err := validateSOAName("mname", s.MName); err != nil {
		return err
	}
	return validateSOAName("rname", s.RName)
}

// validateSOAName rejects names that cannot be a single domain name.
func validateSOAName(field, v string) error {
	if v == "" {
		return nil
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("%s %q must be a single domain name with no spaces", field, v)
	}
	return nil
}

// findSOA returns the live SOA record for zone, if the provider returned one.
// The apex may be represented as "", "@", or the zone name itself.
func findSOA(recs []libdns.Record, zone string) *libdns.RR {
	z := normalizeName(zone)
	for _, rec := range recs {
		rr := rec.RR()
		if !strings.EqualFold(rr.Type, soaType) {
			continue
		}
		switch normalizeName(rr.Name) {
		case "", "@", z:
			r := rr
			return &r
		}
	}
	return nil
}

// parseSOAData parses the zone-file RDATA of an SOA: seven whitespace-separated
// fields (MNAME RNAME SERIAL REFRESH RETRY EXPIRE MINIMUM).
func parseSOAData(data string) (soaFields, bool) {
	f := strings.Fields(data)
	if len(f) != 7 {
		return soaFields{}, false
	}
	var nums [5]uint32
	for i, tok := range f[2:] {
		n, err := strconv.ParseUint(tok, 10, 32)
		if err != nil {
			return soaFields{}, false
		}
		nums[i] = uint32(n)
	}
	return soaFields{
		mname:   f[0],
		rname:   f[1],
		serial:  nums[0],
		refresh: nums[1],
		retry:   nums[2],
		expire:  nums[3],
		minimum: nums[4],
	}, true
}

// desired builds the SOA record this spec wants in the zone, given the live SOA
// (which may be nil). The second return value reports whether the live SOA
// already matches; when it is false the caller should leave the zone alone.
func (s *SOASpec) desired(defaultTTL time.Duration, live *libdns.RR) (libdns.Record, bool, error) {
	var lf soaFields
	haveLive := false
	if live != nil {
		if f, ok := parseSOAData(live.Data); ok {
			lf, haveLive = f, true
		}
	}

	mname := s.MName
	if mname == "" {
		if !haveLive {
			return nil, false, errors.New("mname is required: the provider did not return the existing SOA to inherit it from")
		}
		mname = lf.mname
	}
	rname := s.RName
	if rname == "" {
		if !haveLive {
			return nil, false, errors.New("rname is required: the provider did not return the existing SOA to inherit it from")
		}
		rname = lf.rname
	}

	refresh := atLeast(durOr(s.Refresh, soaDefaultRefresh), s.MinTTL)
	retry := atLeast(durOr(s.Retry, soaDefaultRetry), s.MinTTL)
	expire := atLeast(durOr(s.Expire, soaDefaultExpire), s.MinTTL)
	minimum := atLeast(durOr(s.Minimum, soaDefaultMinimum), s.MinTTL)
	ttl := time.Duration(s.TTL)
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl == 0 {
		ttl = soaDefaultTTL
	}
	ttl = atLeast(ttl, s.MinTTL)

	var liveTTL uint32
	if live != nil {
		liveTTL = seconds(live.TTL)
	}

	changed := !haveLive ||
		mname != lf.mname ||
		rname != lf.rname ||
		seconds(refresh) != lf.refresh ||
		seconds(retry) != lf.retry ||
		seconds(expire) != lf.expire ||
		seconds(minimum) != lf.minimum ||
		seconds(ttl) != liveTTL ||
		(s.Serial != 0 && s.Serial != lf.serial)

	var serial uint32
	switch {
	case s.Serial != 0:
		serial = s.Serial
	case !haveLive:
		serial = uint32(time.Now().Unix())
	case changed:
		serial = lf.serial + 1
	default:
		serial = lf.serial
	}

	data := fmt.Sprintf("%s %s %d %d %d %d %d",
		mname, rname, serial,
		seconds(refresh), seconds(retry), seconds(expire), seconds(minimum))
	rr := libdns.RR{Name: "@", TTL: ttl, Type: soaType, Data: data}
	return rr, changed, nil
}

// durOr returns d, or def when d is unset (0).
func durOr(d caddy.Duration, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return time.Duration(d)
}

// atLeast raises d to min when min is set and d is smaller.
func atLeast(d time.Duration, min caddy.Duration) time.Duration {
	if min > 0 && d < time.Duration(min) {
		return time.Duration(min)
	}
	return d
}

// seconds truncates a duration to whole seconds, as stored in an SOA.
func seconds(d time.Duration) uint32 {
	if d < 0 {
		return 0
	}
	return uint32(d / time.Second)
}
