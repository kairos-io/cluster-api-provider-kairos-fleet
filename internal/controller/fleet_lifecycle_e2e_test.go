/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"testing"

	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/kairos-io/cluster-api-provider-kairos-fleet/api/v1alpha1"
)

// TestFleetLifecycle_E2E drives the full KairosFleetMachine lifecycle against a
// faithful in-memory AuroraBoot server using the REAL net/http fleet client (the
// default client factory, not the fake interface). It is the end-to-end integration
// test for the provider: claim -> apply bootstrap cloud-config -> reboot -> rejoin ->
// providerID/addresses/provisioned, then delete -> release. It exercises the actual
// HTTP request/response and error wiring the controller depends on, deterministically
// (no real Kairos nodes; those are covered by the manual dome lab procedure in the
// docs).
func TestFleetLifecycle_E2E(t *testing.T) {
	// One claimable node in the target group.
	ab := newFakeAuroraBoot(map[string][]fakeNode{
		"workers": {{ID: "node-e2e-1", MachineID: "mid-e2e", Hostname: "worker-e2e",
			Addresses: []fakeNodeAddress{
				{Type: "InternalIP", Address: "10.0.0.7"},
				// A type outside Cluster API's enum: AuroraBoot passes whatever the
				// agent reports, and the apiserver would reject the status update
				// if the provider did the same.
				{Type: "LinkLocalIP", Address: "169.254.0.1"},
			}}},
	})
	defer ab.Close()

	// Standard object graph, with the InfraCluster pointed at the fake server so the
	// real client (fleet.New via the default factory) talks to it.
	objs := testFixture(true)
	for _, o := range objs {
		if fc, ok := o.(*infrav1.KairosFleetCluster); ok {
			fc.Spec.AuroraBoot.URL = ab.URL()
		}
	}
	s := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&infrav1.KairosFleetMachine{}, &infrav1.KairosFleetCluster{}).
		Build()

	// No FleetClientFactory set -> DefaultFleetClientFactory -> the real HTTP client.
	r := &KairosFleetMachineReconciler{Client: c, Scheme: s}

	// Drive the state machine to provisioned. The staged claim -> apply -> reboot ->
	// rejoin takes several level-triggered reconciles.
	kfm := getKFM(t, c)
	for i := 0; i < 10 && !ptr.Deref(kfm.Status.Initialization.Provisioned, false); i++ {
		reconcileKFM(t, r)
		kfm = getKFM(t, c)
	}

	if !ptr.Deref(kfm.Status.Initialization.Provisioned, false) {
		t.Fatalf("machine did not provision; conditions=%+v annotations=%+v", kfm.Status.Conditions, kfm.Annotations)
	}
	if got := ptr.Deref(kfm.Spec.ProviderID, ""); got != providerIDPrefix+"node-e2e-1" {
		t.Fatalf("providerID = %q, want %snode-e2e-1", got, providerIDPrefix)
	}
	// The addresses the node reported to AuroraBoot reach status.addresses through
	// the real HTTP client, led by the hostname; the type Cluster API does not
	// define is dropped rather than passed through.
	wantAddrs := []clusterv1.MachineAddress{
		{Type: clusterv1.MachineHostName, Address: "worker-e2e"},
		{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
	}
	if len(kfm.Status.Addresses) != len(wantAddrs) {
		t.Fatalf("addresses = %+v, want %+v", kfm.Status.Addresses, wantAddrs)
	}
	for i := range wantAddrs {
		if kfm.Status.Addresses[i] != wantAddrs[i] {
			t.Fatalf("addresses[%d] = %+v, want %+v", i, kfm.Status.Addresses[i], wantAddrs[i])
		}
	}

	// The real client drove the AuroraBoot lifecycle: the bootstrap cloud-config was
	// applied verbatim and the node was rebooted to pick it up.
	if got := ab.applied["node-e2e-1"]; got != "#cloud-config\n" {
		t.Errorf("apply-cloud-config carried %q, want the bootstrap secret value", got)
	}
	if !ab.rebooted["node-e2e-1"] {
		t.Error("node was not rebooted after apply-cloud-config")
	}

	// The node-id was recorded on the machine and matches the claimed node.
	if kfm.Annotations[infrav1.NodeIDAnnotation] != "node-e2e-1" {
		t.Errorf("node-id annotation = %q", kfm.Annotations[infrav1.NodeIDAnnotation])
	}

	// Delete -> the node is released back to the pool with the machine's claim key,
	// then the finalizer is removed.
	if err := c.Delete(context.Background(), kfm); err != nil {
		t.Fatalf("delete: %v", err)
	}
	reconcileKFM(t, r)

	if !ab.wasReleased("node-e2e-1", "kfm-uid") {
		t.Error("node was not released with the machine UID on delete")
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(kfm), &infrav1.KairosFleetMachine{}); err == nil {
		t.Error("expected KairosFleetMachine to be gone after the finalizer was cleared")
	}
}
