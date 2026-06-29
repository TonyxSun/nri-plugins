// Copyright 2026 Intel Corporation. All Rights Reserved.
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

package topologyaware

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// newBurstableCtr builds a Burstable mockContainer whose 2000m CPU request maps
// to a pure shared allocation. PrettyName == name; GetID == id.
func newBurstableCtr(name, id string) *mockContainer {
	reqs := v1.ResourceRequirements{
		Requests: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse("2000m"),
			v1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Limits: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse("2000m"),
			v1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
	return &mockContainer{
		name:                                  name, // mockContainer.PrettyName() returns this verbatim
		returnValueForGetID:                   id,
		returnValueForQOSClass:                v1.PodQOSBurstable,
		returnValueForGetResourceRequirements: reqs,
		pod:                                   &mockPod{returnValueFotGetQOSClass: v1.PodQOSBurstable},
	}
}

// TestAllocateResourcesReclaimsStaleInstance is the core regression test: a
// restarted container (same PrettyName, new CRI ID) must not leave the
// predecessor's grant behind and double-charge the shared pool.
func TestAllocateResourcesReclaimsStaleInstance(t *testing.T) {
	policy := newServerPolicy(t)
	old := newBurstableCtr("ns/pod/sidecar", "old-id")
	cur := newBurstableCtr("ns/pod/sidecar", "new-id")

	if err := policy.AllocateResources(old); err != nil {
		t.Fatalf("allocate(old): %v", err)
	}
	// Baseline: shared CPU charged for exactly one instance of this container.
	single := policy.root.GrantedSharedCPU()
	if single <= 0 {
		t.Fatalf("granted shared after first alloc = %dm, want > 0 (shared/fractional alloc expected)", single)
	}

	if err := policy.AllocateResources(cur); err != nil { // restarted instance
		t.Fatalf("allocate(new): %v", err)
	}

	if got := len(policy.allocations.grants); got != 1 {
		t.Errorf("len(grants) = %d, want 1 (stale predecessor not reclaimed)", got)
	}
	if _, ok := policy.allocations.grants[old.GetID()]; ok {
		t.Errorf("stale grant %q still present", old.GetID())
	}
	if _, ok := policy.allocations.grants[cur.GetID()]; !ok {
		t.Errorf("live grant %q missing", cur.GetID())
	}
	// The restart must not double-charge: total shared CPU equals the
	// single-instance baseline, not twice it (pre-fix: 2*single).
	if got := policy.root.GrantedSharedCPU(); got != single {
		t.Errorf("granted shared after restart = %dm, want %dm (double-charge regression)", got, single)
	}
}

// TestAllocateResourcesNoDoubleChargeLiveThenStale allocates the live instance
// FIRST and the stale predecessor SECOND (the adversarial order map iteration
// can produce). It locks the primary guarantee - no double-charge in any order
// - and documents the accepted last-write-wins outcome: in this order the
// last-allocated (stale) id is the survivor.
func TestAllocateResourcesNoDoubleChargeLiveThenStale(t *testing.T) {
	policy := newServerPolicy(t)
	live := newBurstableCtr("ns/pod/ctr", "live-id")
	stale := newBurstableCtr("ns/pod/ctr", "stale-id")

	if err := policy.AllocateResources(live); err != nil {
		t.Fatalf("allocate(live): %v", err)
	}
	single := policy.root.GrantedSharedCPU()
	if single <= 0 {
		t.Fatalf("granted shared after first alloc = %dm, want > 0", single)
	}

	if err := policy.AllocateResources(stale); err != nil {
		t.Fatalf("allocate(stale): %v", err)
	}

	if got := len(policy.allocations.grants); got != 1 {
		t.Errorf("len(grants) = %d, want 1 (no double charge in any order)", got)
	}
	if got := policy.root.GrantedSharedCPU(); got != single {
		t.Errorf("granted shared = %dm, want %dm (no double charge in any order)", got, single)
	}
	// Documented last-write-wins: the second-allocated id survives. If a future
	// change makes the choice deterministic (live always wins), update this.
	if _, ok := policy.allocations.grants["stale-id"]; !ok {
		t.Errorf("expected last-allocated id to survive (documented last-write-wins)")
	}
}

// TestAllocateResourcesReclaimsMultipleStaleInstances covers the multi-leak case:
// a container that leaked more than once leaves several same-PrettyName
// predecessors. releaseStaleInstances collects all matches before releasing
// (releasePool mutates the grant map mid-range), so a single allocation of the
// new instance must converge to exactly one grant and single-instance shared CPU.
func TestAllocateResourcesReclaimsMultipleStaleInstances(t *testing.T) {
	policy := newServerPolicy(t)
	old1 := newBurstableCtr("ns/pod/ctr", "old1")
	old2 := newBurstableCtr("ns/pod/ctr", "old2")
	cur := newBurstableCtr("ns/pod/ctr", "new-id")

	// Inject two leaked predecessors; allocatePool inserts and charges each.
	if _, err := policy.allocatePool(old1, ""); err != nil {
		t.Fatalf("allocatePool(old1): %v", err)
	}
	single := policy.root.GrantedSharedCPU()
	if single <= 0 {
		t.Fatalf("granted shared after first inject = %dm, want > 0", single)
	}
	if _, err := policy.allocatePool(old2, ""); err != nil {
		t.Fatalf("allocatePool(old2): %v", err)
	}
	if got := len(policy.allocations.grants); got != 2 {
		t.Fatalf("len(grants) = %d, want 2 after injecting two predecessors", got)
	}

	if err := policy.AllocateResources(cur); err != nil { // restarted instance
		t.Fatalf("allocate(cur): %v", err)
	}

	if got := len(policy.allocations.grants); got != 1 {
		t.Errorf("len(grants) = %d, want 1 (all stale predecessors reclaimed)", got)
	}
	if _, ok := policy.allocations.grants["new-id"]; !ok {
		t.Errorf("live grant %q missing", "new-id")
	}
	if got := policy.root.GrantedSharedCPU(); got != single {
		t.Errorf("granted shared = %dm, want %dm (one instance after reclaim)", got, single)
	}
}

// TestReleaseStaleInstancesReclaimsPredecessor is the unit-level fallback for the
// regression: a same-name/different-id grant is released and its supply
// reclaimed. It calls the new helper directly and injects the predecessor grant
// via allocatePool, so it does not depend on driving AllocateResources
// end-to-end through the mocks.
func TestReleaseStaleInstancesReclaimsPredecessor(t *testing.T) {
	policy := newServerPolicy(t)
	old := newBurstableCtr("ns/pod/ctr", "old-id")
	cur := newBurstableCtr("ns/pod/ctr", "new-id")

	// Inject the predecessor's grant: allocatePool inserts it into
	// p.allocations.grants and charges supply, so GrantedSharedCPU is non-zero.
	if _, err := policy.allocatePool(old, ""); err != nil {
		t.Fatalf("allocatePool(old): %v", err)
	}
	if got := policy.root.GrantedSharedCPU(); got <= 0 {
		t.Fatalf("granted shared after inject = %dm, want > 0", got)
	}

	policy.releaseStaleInstances(cur) // same name, different id -> reclaim old

	if got := len(policy.allocations.grants); got != 0 {
		t.Errorf("len(grants) = %d, want 0 (predecessor not reclaimed)", got)
	}
	if got := policy.root.GrantedSharedCPU(); got != 0 {
		t.Errorf("granted shared after reclaim = %dm, want 0m (supply not returned)", got)
	}
}

// TestReleaseStaleInstancesSkipsSameID proves the self-exclusion guard
// (other.GetID() != container.GetID()): the helper must NOT release the grant of
// the very container being (re)allocated. WITHOUT the guard this fails (len
// would be 0, granted 0). This is the real "do not misfire" regression
// protection.
func TestReleaseStaleInstancesSkipsSameID(t *testing.T) {
	policy := newServerPolicy(t)
	ctr := newBurstableCtr("ns/pod/ctr", "same-id")

	// allocatePool inserts the grant into p.allocations.grants and charges supply.
	if _, err := policy.allocatePool(ctr, ""); err != nil {
		t.Fatalf("allocatePool: %v", err)
	}
	single := policy.root.GrantedSharedCPU()
	if single <= 0 {
		t.Fatalf("granted shared after inject = %dm, want > 0", single)
	}

	policy.releaseStaleInstances(ctr) // same name AND same id -> must be skipped

	if got := len(policy.allocations.grants); got != 1 {
		t.Errorf("len(grants) = %d, want 1 (guard dropped the container being allocated)", got)
	}
	if _, ok := policy.allocations.grants["same-id"]; !ok {
		t.Errorf("guard dropped the grant for the container being allocated")
	}
	if got := policy.root.GrantedSharedCPU(); got != single {
		t.Errorf("granted shared = %dm, want %dm (guard released own supply)", got, single)
	}
}

// TestReleaseStaleInstancesSkipsDifferentName proves the name half of the
// predicate (other.PrettyName() == name): a grant held by an unrelated
// container under a DIFFERENT PrettyName must never be reclaimed. WITHOUT the
// name check the helper would release every other-id grant and de-account a
// live, unrelated container.
func TestReleaseStaleInstancesSkipsDifferentName(t *testing.T) {
	policy := newServerPolicy(t)
	a := newBurstableCtr("ns/podA/ctr", "a1")
	b := newBurstableCtr("ns/podB/ctr", "b1")

	// allocatePool inserts a's grant into p.allocations.grants and charges supply.
	if _, err := policy.allocatePool(a, ""); err != nil {
		t.Fatalf("allocatePool(a): %v", err)
	}
	single := policy.root.GrantedSharedCPU()
	if single <= 0 {
		t.Fatalf("granted shared after inject = %dm, want > 0", single)
	}

	policy.releaseStaleInstances(b) // different name -> must not touch a's grant

	if got := len(policy.allocations.grants); got != 1 {
		t.Errorf("len(grants) = %d, want 1 (unrelated grant reclaimed)", got)
	}
	if _, ok := policy.allocations.grants["a1"]; !ok {
		t.Errorf("grant for unrelated container %q dropped", "a1")
	}
	if got := policy.root.GrantedSharedCPU(); got != single {
		t.Errorf("granted shared = %dm, want %dm (unrelated supply released)", got, single)
	}
}

// TestUpdateResourcesPathUnchanged is a structural guard: an in-place update
// keeps exactly one grant under the same id with a single charge. It does not
// by itself prove releaseStaleInstances never runs on the update path - that
// follows from UpdateResources calling allocateResources directly, not
// AllocateResources. It passes both pre-fix and post-fix by design.
func TestUpdateResourcesPathUnchanged(t *testing.T) {
	policy := newServerPolicy(t)
	ctr := newBurstableCtr("ns/pod/ctr", "same-id")

	if err := policy.AllocateResources(ctr); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	single := policy.root.GrantedSharedCPU()
	if err := policy.UpdateResources(ctr); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := len(policy.allocations.grants); got != 1 {
		t.Errorf("len(grants) = %d, want 1 after in-place update", got)
	}
	if _, ok := policy.allocations.grants["same-id"]; !ok {
		t.Errorf("grant for same-id dropped by update")
	}
	if got := policy.root.GrantedSharedCPU(); got != single {
		t.Errorf("granted shared after update = %dm, want %dm", got, single)
	}
}
