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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/kairos-io/cluster-api-provider-kairos-fleet/api/v1alpha1"
	"github.com/kairos-io/cluster-api-provider-kairos-fleet/internal/fleet"
)

const (
	testNS       = "default"
	testNodeID   = "node-abc"
	testHostname = "worker-1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("core scheme: %v", err)
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		t.Fatalf("cluster-api scheme: %v", err)
	}
	if err := infrav1.AddToScheme(s); err != nil {
		t.Fatalf("infra scheme: %v", err)
	}
	return s
}

// testFixture builds the CAPI object graph a KairosFleetMachine reconcile needs.
func testFixture(bootstrapReady bool) []client.Object {
	fleetCluster := &infrav1.KairosFleetCluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: "fc"},
		Spec: infrav1.KairosFleetClusterSpec{
			ControlPlaneEndpoint: &clusterv1.APIEndpoint{Host: "10.0.0.1", Port: 6443},
			AuroraBoot: infrav1.AuroraBootConnection{
				URL:                 "https://auroraboot.example",
				AdminTokenSecretRef: infrav1.LocalSecretReference{Name: "ab-token"},
			},
		},
	}
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: "cl"},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{Name: "fc", Kind: "KairosFleetCluster"},
		},
	}
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS, Name: "m", UID: "machine-uid",
			Labels: map[string]string{clusterv1.ClusterNameLabel: "cl"},
		},
		Spec: clusterv1.MachineSpec{ClusterName: "cl"},
	}
	if bootstrapReady {
		machine.Spec.Bootstrap.DataSecretName = ptr.To("boot")
	}
	fleetMachine := &infrav1.KairosFleetMachine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS, Name: "kfm", UID: "kfm-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(), Kind: "Machine",
				Name: "m", UID: "machine-uid",
			}},
		},
		Spec: infrav1.KairosFleetMachineSpec{Group: "workers"},
	}
	objs := []client.Object{fleetCluster, cluster, machine, fleetMachine,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: "ab-token"},
			Data:       map[string][]byte{adminTokenSecretKey: []byte("s3cret")},
		},
	}
	if bootstrapReady {
		objs = append(objs, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: "boot"},
			Data:       map[string][]byte{bootstrapDataSecretKey: []byte("#cloud-config\n")},
		})
	}
	return objs
}

func newReconciler(t *testing.T, fc fleet.Client, objs []client.Object) (*KairosFleetMachineReconciler, client.Client) {
	s := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&infrav1.KairosFleetMachine{}, &infrav1.KairosFleetCluster{}).
		Build()
	return &KairosFleetMachineReconciler{
		Client:             c,
		Scheme:             s,
		FleetClientFactory: func(_, _ string) fleet.Client { return fc },
	}, c
}

func reconcileKFM(t *testing.T, r *KairosFleetMachineReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: "kfm"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return res
}

func getKFM(t *testing.T, c client.Client) *infrav1.KairosFleetMachine {
	t.Helper()
	kfm := &infrav1.KairosFleetMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: "kfm"}, kfm); err != nil {
		t.Fatalf("get KFM: %v", err)
	}
	return kfm
}

func TestMachineReconcile_WaitsForBootstrap(t *testing.T) {
	r, c := newReconciler(t, &fleet.FakeClient{}, testFixture(false))
	reconcileKFM(t, r)

	kfm := getKFM(t, c)
	if kfm.Annotations[infrav1.NodeIDAnnotation] != "" {
		t.Fatalf("did not expect a node to be claimed before bootstrap is ready")
	}
	if provisioned := ptr.Deref(kfm.Status.Initialization.Provisioned, false); provisioned {
		t.Fatalf("expected not provisioned while waiting for bootstrap")
	}
}

func TestMachineReconcile_ClaimsThenApplies(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	// First reconcile: claims and records the node-id annotation, then requeues.
	if res := reconcileKFM(t, r); !res.Requeue {
		t.Fatalf("expected requeue after claim")
	}
	if len(fc.Claims) != 1 || fc.Claims[0].ClaimKey != "kfm-uid" {
		t.Fatalf("expected one claim with claimKey=kfm-uid, got %+v", fc.Claims)
	}
	if got := getKFM(t, c).Annotations[infrav1.NodeIDAnnotation]; got != testNodeID {
		t.Fatalf("node-id annotation = %q, want %q", got, testNodeID)
	}

	// Second reconcile: applies the bootstrap cloud-config.
	reconcileKFM(t, r)
	if len(fc.Applies) != 1 || fc.Applies[0].NodeID != testNodeID {
		t.Fatalf("expected one apply-cloud-config to %s, got %+v", testNodeID, fc.Applies)
	}
	if getKFM(t, c).Annotations[cloudConfigAppliedAnnotation] != "true" {
		t.Fatalf("expected cloud-config-applied annotation set")
	}
}

func TestMachineReconcile_ProvisionsWhenOnline(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{{Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted}}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	// Claim -> apply -> provisioned.
	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // provisioned

	kfm := getKFM(t, c)
	if !ptr.Deref(kfm.Status.Initialization.Provisioned, false) {
		t.Fatalf("expected provisioned=true, conditions=%+v", kfm.Status.Conditions)
	}
	wantPID := providerIDPrefix + testNodeID
	if ptr.Deref(kfm.Spec.ProviderID, "") != wantPID {
		t.Fatalf("providerID = %q, want %q", ptr.Deref(kfm.Spec.ProviderID, ""), wantPID)
	}
	if len(kfm.Status.Addresses) != 1 || kfm.Status.Addresses[0].Address != testHostname {
		t.Fatalf("expected hostname address %q, got %+v", testHostname, kfm.Status.Addresses)
	}
}

func TestMachineReconcile_NoCapacityRequeues(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return nil, fleet.NoCapacityError()
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	res := reconcileKFM(t, r)
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a timed requeue on no-capacity")
	}
	if getKFM(t, c).Annotations[infrav1.NodeIDAnnotation] != "" {
		t.Fatalf("did not expect a node-id annotation when no capacity")
	}
}

func TestMachineReconcile_ReleasesOnDelete(t *testing.T) {
	fc := &fleet.FakeClient{}
	objs := testFixture(true)
	// Mark the KFM claimed and deleting with the finalizer present.
	for _, o := range objs {
		if kfm, ok := o.(*infrav1.KairosFleetMachine); ok {
			kfm.Annotations = map[string]string{infrav1.NodeIDAnnotation: testNodeID}
			kfm.Finalizers = []string{infrav1.KairosFleetMachineFinalizer}
			now := metav1.Now()
			kfm.DeletionTimestamp = &now
		}
	}
	r, c := newReconciler(t, fc, objs)

	reconcileKFM(t, r)
	if len(fc.Releases) != 1 || fc.Releases[0].NodeID != testNodeID || fc.Releases[0].ClaimKey != "kfm-uid" {
		t.Fatalf("expected one release of %s with claimKey=kfm-uid, got %+v", testNodeID, fc.Releases)
	}
	// Finalizer removed -> the object is gone from the fake tracker.
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: "kfm"}, &infrav1.KairosFleetMachine{})
	if err == nil {
		t.Fatalf("expected KFM to be removed after finalizer cleared")
	}
}
