// Copyright The NRI Plugins Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"testing"
	"time"
)

func TestStatusStartupJitterEmpty(t *testing.T) {
	if got := statusStartupJitter(""); got != 0 {
		t.Fatalf("statusStartupJitter(\"\") = %v, want 0", got)
	}
}

func TestStatusStartupJitterRange(t *testing.T) {
	// Default max is 10s; pin it so this test is independent of flag state.
	old := statusStartupJitterMax
	statusStartupJitterMax = 10 * time.Second
	t.Cleanup(func() { statusStartupJitterMax = old })

	for _, name := range []string{
		"node-a",
		"node-b",
		"worker-1",
		"atla-prod-swarm07-worker-abc123",
	} {
		got := statusStartupJitter(name)
		if got < 0 || got >= statusStartupJitterMax {
			t.Errorf("statusStartupJitter(%q) = %v, want in [0, %v)", name, got, statusStartupJitterMax)
		}
	}
}

func TestStatusStartupJitterStable(t *testing.T) {
	old := statusStartupJitterMax
	statusStartupJitterMax = 10 * time.Second
	t.Cleanup(func() { statusStartupJitterMax = old })

	const name = "test-node-42"
	// FNV-1a 64-bit of "test-node-42" mod 10s (pinned so algorithm changes fail loudly).
	want := 7086773805 * time.Nanosecond

	d1 := statusStartupJitter(name)
	d2 := statusStartupJitter(name)
	if d1 != d2 {
		t.Fatalf("statusStartupJitter(%q) unstable: %v vs %v", name, d1, d2)
	}
	if d1 != want {
		t.Fatalf("statusStartupJitter(%q) = %v, want %v", name, d1, want)
	}
}

func TestStatusStartupJitterDisabled(t *testing.T) {
	old := statusStartupJitterMax
	statusStartupJitterMax = 0
	t.Cleanup(func() { statusStartupJitterMax = old })

	if got := statusStartupJitter("node-a"); got != 0 {
		t.Fatalf("statusStartupJitter with max=0 = %v, want 0", got)
	}
}

func TestStatusStartupJitterDeterministicAcrossCalls(t *testing.T) {
	names := []string{"alpha", "beta", "gamma", "delta"}
	first := make(map[string]time.Duration, len(names))
	for _, n := range names {
		first[n] = statusStartupJitter(n)
	}
	for i := 0; i < 10; i++ {
		for _, n := range names {
			if got := statusStartupJitter(n); got != first[n] {
				t.Fatalf("iteration %d: statusStartupJitter(%q) = %v, want %v", i, n, got, first[n])
			}
		}
	}
}
