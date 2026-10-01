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

// Package caddyhostdns is the root package of the module. It exists so that a
// Caddy build can import the module root, e.g.
//
//	xcaddy build --with github.com/SvenDowideit/caddy-host-dns
//
// and have the dns_records app registered.
package caddyhostdns

import (
	_ "github.com/SvenDowideit/caddy-host-dns/dnsrec"
)
