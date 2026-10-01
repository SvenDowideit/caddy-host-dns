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

// Register the DNS provider modules under test. To test your own provider,
// add its blank import here (or in your own test file) and call
// conformance.Run with its config.
import (
	_ "github.com/caddy-dns/powerdns"
	_ "github.com/caddy-dns/rfc2136"
)
