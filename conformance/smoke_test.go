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

//go:build conformance

package conformance_test

import (
	"testing"

	"github.com/SvenDowideit/caddy-host-dns/conformance"
	"github.com/libdns/libdns/libdnstest/example"
)

// TestSmoke proves the conformance wrapper runs the libdns suite correctly,
// using the in-memory example provider so it needs no credentials. CI runs
// this with `go test -tags conformance ./conformance/...`.
func TestSmoke(t *testing.T) {
	conformance.RunProvider(t, example.New("example.com."), "example.com.")
}
