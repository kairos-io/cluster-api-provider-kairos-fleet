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
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	infrav1 "github.com/kairos-io/cluster-api-provider-kairos-fleet/api/v1alpha1"
	"github.com/kairos-io/cluster-api-provider-kairos-fleet/internal/fleet"
)

const (
	testNS       = "default"
	testNodeID   = "node-abc"
	testHostname = "worker-1"

	// fakeApplyCommandID is the command ID fleet.FakeClient.ApplyCloudConfig
	// returns; the reconciler records it and reads that command's outcome back.
	fakeApplyCommandID = "fake-cmd"

	// fakeRebootCommandID is the same for fleet.FakeClient.Reboot.
	fakeRebootCommandID = "fake-reboot"
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

	// First reconcile: claims and records the node-id annotation (the watch on the
	// annotation update drives the next reconcile).
	reconcileKFM(t, r)
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

func TestMachineReconcile_RebootsThenProvisions(t *testing.T) {
	// Heartbeat clearly after the reboot request, so rejoin is detected.
	hb := time.Now().Add(time.Hour)
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline, LastHeartbeat: &hb}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			// ID matches what FakeClient.ApplyCloudConfig returned, so the reconciler
			// reads the outcome of the command it actually queued.
			return []fleet.Command{{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted}}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	// Claim -> apply -> reboot -> provisioned.
	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // reboot (apply command completed)
	if len(fc.Reboots) != 1 || fc.Reboots[0] != testNodeID {
		t.Fatalf("expected one reboot of %s, got %+v", testNodeID, fc.Reboots)
	}
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

func TestMachineReconcile_WaitsForApplyBeforeReboot(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			// apply-cloud-config still running -> must not reboot yet.
			return []fleet.Command{{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseRunning}}, nil
		},
	}
	r, _ := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // apply command not complete -> no reboot
	if len(fc.Reboots) != 0 {
		t.Fatalf("did not expect a reboot before apply-cloud-config completes, got %+v", fc.Reboots)
	}
}

func TestMachineReconcile_ResolvesGroupNameBeforeClaim(t *testing.T) {
	// testFixture sets spec.group to the human-friendly name "workers"; the fake
	// AuroraBoot group resolves that name to a distinct UUID, and the reconciler must
	// claim using the resolved id, not the raw spec.group value.
	const resolvedGroupID = "grp-uuid-workers"
	fc := &fleet.FakeClient{
		ResolveGroupIDFunc: func(_ context.Context, ref string) (string, error) {
			if ref != "workers" {
				t.Fatalf("ResolveGroupID called with %q, want %q", ref, "workers")
			}
			return resolvedGroupID, nil
		},
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r)

	if len(fc.Claims) != 1 {
		t.Fatalf("expected one claim, got %+v", fc.Claims)
	}
	if fc.Claims[0].GroupID != resolvedGroupID {
		t.Fatalf("Claim group = %q, want the resolved group id %q", fc.Claims[0].GroupID, resolvedGroupID)
	}
	if got := getKFM(t, c).Annotations[infrav1.NodeIDAnnotation]; got != testNodeID {
		t.Fatalf("node-id annotation = %q, want %q", got, testNodeID)
	}
}

func TestMachineReconcile_GroupNotFoundRequeues(t *testing.T) {
	fc := &fleet.FakeClient{
		ResolveGroupIDFunc: func(_ context.Context, _ string) (string, error) {
			return "", fleet.NotFoundError()
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	res := reconcileKFM(t, r)
	if res.RequeueAfter == 0 {
		t.Fatalf("expected a timed requeue when the group is not found")
	}
	if len(fc.Claims) != 0 {
		t.Fatalf("did not expect a claim attempt when group resolution fails, got %+v", fc.Claims)
	}
	kfm := getKFM(t, c)
	if kfm.Annotations[infrav1.NodeIDAnnotation] != "" {
		t.Fatalf("did not expect a node-id annotation when the group is not found")
	}
	if provisioned := ptr.Deref(kfm.Status.Initialization.Provisioned, true); provisioned {
		t.Fatalf("expected not provisioned when the group is not found")
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

// A node whose phonehome policy rejects apply-cloud-config leaves a Failed command
// behind forever: AuroraBoot never prunes a node's command history. Selecting the
// first apply-cloud-config would let that one failure shadow every later success,
// so the reconciler must read back the command it queued.
func TestMachineReconcile_IgnoresAnEarlierFailedApply(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	hb := time.Now().Add(time.Hour)
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline, LastHeartbeat: &hb}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{
				{
					ID: "stale-cmd", Command: fleet.CommandApplyCloudConfig,
					Phase:  fleet.CommandPhaseFailed,
					Result: `command "apply-cloud-config" is not permitted by the phonehome policy`,
					// Deliberately first in the slice and older.
					CreatedAt: &older,
				},
				{
					ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig,
					Phase: fleet.CommandPhaseCompleted, CreatedAt: &newer,
				},
			}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // read back OUR command -> completed -> reboot
	if len(fc.Reboots) != 1 {
		kfm := getKFM(t, c)
		t.Fatalf("expected the stale failure to be ignored and a reboot issued, got %+v, conditions=%+v", fc.Reboots, kfm.Status.Conditions)
	}
	reconcileKFM(t, r) // provisioned
	if !ptr.Deref(getKFM(t, c).Status.Initialization.Provisioned, false) {
		t.Fatalf("expected provisioned=true")
	}
}

// With no recorded command ID (a machine first reconciled by an older build) the
// newest apply-cloud-config wins, not the first one in the list.
func TestApplyState_WithoutACommandIDPicksTheNewest(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	fc := &fleet.FakeClient{
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{
				{ID: "old", Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseFailed, Result: "denied", CreatedAt: &older},
				{ID: "new", Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted, CreatedAt: &newer},
			}, nil
		},
	}
	r := &KairosFleetMachineReconciler{}
	completed, failed, msg := r.commandState(context.Background(), fc, testNodeID, "", fleet.CommandApplyCloudConfig)
	if !completed || failed {
		t.Fatalf("completed=%v failed=%v msg=%q, want the newest (completed) command to win", completed, failed, msg)
	}
}

// The no-id fallback must also filter by command kind: a node whose last command
// was a failed reboot must not have that read back as the apply's outcome.
func TestCommandState_WithoutACommandIDFiltersByKind(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	fc := &fleet.FakeClient{
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{
				{ID: "apply", Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted, CreatedAt: &older},
				{ID: "reboot", Command: fleet.CommandReboot, Phase: fleet.CommandPhaseFailed, Result: "denied", CreatedAt: &newer},
			}, nil
		},
	}
	r := &KairosFleetMachineReconciler{}
	if completed, failed, _ := r.commandState(context.Background(), fc, testNodeID, "", fleet.CommandApplyCloudConfig); !completed || failed {
		t.Fatalf("the apply completed; the later failed reboot must not shadow it")
	}
	if _, failed, msg := r.commandState(context.Background(), fc, testNodeID, "", fleet.CommandReboot); !failed || msg != "denied" {
		t.Fatalf("failed=%v msg=%q, want the reboot's own failure", failed, msg)
	}
}

// A failed apply must be retryable: the operator fixes the node's phonehome policy
// and the controller has to issue a fresh command on its own. Marking the machine
// terminally failed, or requeueing without clearing the applied markers, both leave
// it stuck forever.
// expireRetryWindow moves the recorded retry time into the past, standing in for
// the pacing interval elapsing. It also asserts that a window was opened.
func expireRetryWindow(t *testing.T, c client.Client) {
	t.Helper()
	kfm := getKFM(t, c)
	if kfm.Annotations[retryNotBeforeAnnotation] == "" {
		t.Fatalf("expected a failed command to open a retry window, annotations: %v", kfm.Annotations)
	}
	kfm.Annotations[retryNotBeforeAnnotation] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	if err := c.Update(context.Background(), kfm); err != nil {
		t.Fatalf("expiring the retry window: %v", err)
	}
}

func TestMachineReconcile_RetriesAFailedApply(t *testing.T) {
	failing := true
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			phase := fleet.CommandPhaseCompleted
			result := ""
			if failing {
				phase = fleet.CommandPhaseFailed
				result = `command "apply-cloud-config" is not permitted by the phonehome policy`
			}
			return []fleet.Command{{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: phase, Result: result}}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply (1st)
	res := reconcileKFM(t, r)

	if res.RequeueAfter != retryCloudConfigRequeue {
		t.Fatalf("RequeueAfter = %v, want %v so the apply is retried", res.RequeueAfter, retryCloudConfigRequeue)
	}
	kfm := getKFM(t, c)
	if kfm.Status.FailureReason != nil || kfm.Status.FailureMessage != nil {
		t.Fatalf("a rejected apply is recoverable and must not set a terminal failure, got reason=%v message=%v",
			ptr.Deref(kfm.Status.FailureReason, ""), ptr.Deref(kfm.Status.FailureMessage, ""))
	}
	if kfm.Annotations[cloudConfigAppliedAnnotation] != "" || kfm.Annotations[cloudConfigCommandIDAnnotation] != "" {
		t.Fatalf("expected the applied markers to be cleared so a fresh command is queued, got %v", kfm.Annotations)
	}
	if kfm.Annotations[infrav1.NodeIDAnnotation] != testNodeID {
		t.Fatalf("the claim must be kept across a retry, got %q", kfm.Annotations[infrav1.NodeIDAnnotation])
	}

	// Clearing the markers is an update, and the watch answers it with an
	// immediate reconcile. Those passes must hold, not queue another command: a
	// node that refuses at once was otherwise sent a fresh one on every pass.
	for range 3 {
		res = reconcileKFM(t, r)
		if len(fc.Applies) != 1 {
			t.Fatalf("an immediate re-reconcile must not queue another apply-cloud-config inside the retry window, got %d", len(fc.Applies))
		}
		if res.RequeueAfter <= 0 || res.RequeueAfter > retryCloudConfigRequeue {
			t.Fatalf("a held retry must come back when the window closes, got RequeueAfter=%v", res.RequeueAfter)
		}
	}

	// The operator fixes the node and the window passes; the next pass queues a
	// second apply and moves on.
	failing = false
	expireRetryWindow(t, c)
	reconcileKFM(t, r) // apply (2nd)
	if len(fc.Applies) != 2 {
		t.Fatalf("expected a second apply-cloud-config after the retry, got %d", len(fc.Applies))
	}
	reconcileKFM(t, r) // completed -> reboot
	if len(fc.Reboots) != 1 {
		t.Fatalf("expected the retry to reach the reboot step, got %+v", fc.Reboots)
	}
}

// deletingFixture returns the standard object graph with the KairosFleetMachine
// claimed, finalized and deleting, minus the objects named in drop.
func deletingFixture(drop ...string) []client.Object {
	dropped := map[string]bool{}
	for _, name := range drop {
		dropped[name] = true
	}
	kept := []client.Object{}
	for _, o := range testFixture(true) {
		if dropped[o.GetName()] {
			continue
		}
		if kfm, ok := o.(*infrav1.KairosFleetMachine); ok {
			kfm.Annotations = map[string]string{infrav1.NodeIDAnnotation: testNodeID}
			kfm.Finalizers = []string{infrav1.KairosFleetMachineFinalizer}
			now := metav1.Now()
			kfm.DeletionTimestamp = &now
		}
		kept = append(kept, o)
	}
	return kept
}

func assertKFMGone(t *testing.T, c client.Client) {
	t.Helper()
	err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: "kfm"}, &infrav1.KairosFleetMachine{})
	if err == nil {
		t.Fatalf("expected the finalizer to be removed, the KairosFleetMachine is still present")
	}
}

// The KairosFleetCluster controller removes its own finalizer as soon as it is
// deleted, so the InfraCluster can be gone while machines are still terminating.
// Waiting for it would strand the Machine, and the whole Cluster delete behind it.
func TestMachineReconcile_DeletesWhenInfraClusterIsGone(t *testing.T) {
	fc := &fleet.FakeClient{}
	r, c := newReconciler(t, fc, deletingFixture("fc"))

	reconcileKFM(t, r)
	assertKFMGone(t, c)
	if len(fc.Releases) != 0 {
		t.Fatalf("no release is possible without a connection, got %+v", fc.Releases)
	}
}

// Same for the admin-token Secret: once it is gone there is no way left to reach
// AuroraBoot, and no later reconcile will find one.
func TestMachineReconcile_DeletesWhenAdminTokenSecretIsGone(t *testing.T) {
	fc := &fleet.FakeClient{}
	r, c := newReconciler(t, fc, deletingFixture("ab-token"))

	reconcileKFM(t, r)
	assertKFMGone(t, c)
}

// A KairosFleetCluster that never carried a usable connection is just as permanent.
func TestMachineReconcile_DeletesWhenConnectionIsIncomplete(t *testing.T) {
	objs := deletingFixture()
	for _, o := range objs {
		if s, ok := o.(*corev1.Secret); ok && s.Name == "ab-token" {
			s.Data = map[string][]byte{adminTokenSecretKey: []byte("")}
		}
	}
	fc := &fleet.FakeClient{}
	r, c := newReconciler(t, fc, objs)

	reconcileKFM(t, r)
	assertKFMGone(t, c)
}

// A read error that is not a NotFound can succeed on the next attempt, so the
// reconcile must fail loudly instead of removing the finalizer and leaking the
// claim: the node would stay claimed in its group with nothing left to release it.
func TestMachineReconcile_KeepsFinalizerOnTransientReadError(t *testing.T) {
	s := testScheme(t)
	boom := apierrors.NewServiceUnavailable("etcd leader election")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(deletingFixture()...).
		WithStatusSubresource(&infrav1.KairosFleetMachine{}, &infrav1.KairosFleetCluster{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*infrav1.KairosFleetCluster); ok {
					return boom
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	fc := &fleet.FakeClient{}
	r := &KairosFleetMachineReconciler{
		Client:             c,
		Scheme:             s,
		FleetClientFactory: func(_, _ string) fleet.Client { return fc },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: "kfm"},
	})
	if err == nil {
		t.Fatalf("expected a transient read error to surface, not a silent finalizer removal")
	}
	kfm := &infrav1.KairosFleetMachine{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: "kfm"}, kfm); err != nil {
		t.Fatalf("expected the KairosFleetMachine to still be there: %v", err)
	}
	if !controllerutil.ContainsFinalizer(kfm, infrav1.KairosFleetMachineFinalizer) {
		t.Fatalf("expected the finalizer to be kept so the claim is not leaked")
	}
}

// A node enrolled with a phonehome policy that permits apply-cloud-config but not
// reboot completes its apply and then rejects the reboot. Without reading the
// reboot command back, the machine waits on WaitingForNodeRejoin forever, with the
// reason on the node never surfaced.
func TestMachineReconcile_RetriesARejectedReboot(t *testing.T) {
	const rejected = `command "reboot" is not permitted by the phonehome policy`
	rebootRejected := true
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			// Still up on its old boot: Online, but no heartbeat after the reboot.
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			cmds := []fleet.Command{{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted}}
			if rebootRejected {
				cmds = append(cmds, fleet.Command{
					ID: fakeRebootCommandID, Command: fleet.CommandReboot,
					Phase: fleet.CommandPhaseFailed, Result: rejected,
				})
			}
			return cmds, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // reboot (1st)
	res := reconcileKFM(t, r)

	if res.RequeueAfter != retryRebootRequeue {
		t.Fatalf("RequeueAfter = %v, want %v so the reboot is retried", res.RequeueAfter, retryRebootRequeue)
	}
	kfm := getKFM(t, c)
	if kfm.Status.FailureReason != nil || kfm.Status.FailureMessage != nil {
		t.Fatalf("a rejected reboot is recoverable and must not set a terminal failure, got reason=%v message=%v",
			ptr.Deref(kfm.Status.FailureReason, ""), ptr.Deref(kfm.Status.FailureMessage, ""))
	}
	if kfm.Annotations[rebootRequestedAtAnnotation] != "" || kfm.Annotations[rebootCommandIDAnnotation] != "" {
		t.Fatalf("expected the reboot markers to be cleared so a fresh reboot is queued, got %v", kfm.Annotations)
	}
	if kfm.Annotations[cloudConfigAppliedAnnotation] != cloudConfigAppliedValue {
		t.Fatalf("the applied cloud-config must be kept across a reboot retry, got %v", kfm.Annotations)
	}
	ready := meta.FindStatusCondition(kfm.Status.Conditions, clusterv1.ReadyCondition)
	if ready == nil || ready.Reason != "RebootFailed" || !strings.Contains(ready.Message, rejected) {
		t.Fatalf("expected the node's rejection on the Ready condition, got %+v", ready)
	}

	// As for apply-cloud-config: the immediate reconciles that follow the marker
	// change must hold rather than reboot the node again on every pass.
	for range 3 {
		res = reconcileKFM(t, r)
		if len(fc.Reboots) != 1 {
			t.Fatalf("an immediate re-reconcile must not queue another reboot inside the retry window, got %+v", fc.Reboots)
		}
		if res.RequeueAfter <= 0 || res.RequeueAfter > retryRebootRequeue {
			t.Fatalf("a held retry must come back when the window closes, got RequeueAfter=%v", res.RequeueAfter)
		}
	}

	// The operator fixes the policy and the window passes; the next pass queues a
	// second reboot and the node rejoins.
	rebootRejected = false
	expireRetryWindow(t, c)
	reconcileKFM(t, r) // reboot (2nd)
	if len(fc.Reboots) != 2 {
		t.Fatalf("expected a second reboot after the retry, got %+v", fc.Reboots)
	}
	hb := time.Now().Add(time.Hour)
	fc.GetNodeFunc = func(_ context.Context, _ string) (*fleet.Node, error) {
		return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline, LastHeartbeat: &hb}, nil
	}
	reconcileKFM(t, r) // provisioned
	if !ptr.Deref(getKFM(t, c).Status.Initialization.Provisioned, false) {
		t.Fatalf("expected the machine to provision once the reboot went through")
	}
}

// AuroraBoot never prunes a node's command history, so a reboot that failed on an
// earlier attempt is still in the list. The controller records the ID of the reboot
// it queued and reads that exact command back, so an earlier failure cannot shadow
// the current attempt and put the machine in a reboot loop. Same guarantee
// apply-cloud-config already had, see TestMachineReconcile_IgnoresAnEarlierFailedApply.
func TestMachineReconcile_IgnoresAnEarlierFailedReboot(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			// Still up on its old boot: Online with no heartbeat past the reboot, so
			// the outcome of the reboot command is what decides between waiting and
			// retrying. Offline would short-circuit that, see
			// TestMachineReconcile_DoesNotRetryARebootWhileTheNodeIsAway.
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{
				{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted},
				{ID: fakeRebootCommandID, Command: fleet.CommandReboot, Phase: fleet.CommandPhaseRunning},
				// Neither reboot carries a created_at, so there is nothing to order
				// them by and the no-id fallback picks this one. Matching on the
				// recorded ID is the only thing that tells them apart.
				{
					ID: "stale-reboot", Command: fleet.CommandReboot,
					Phase:  fleet.CommandPhaseFailed,
					Result: `command "reboot" is not permitted by the phonehome policy`,
				},
			}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // reboot
	res := reconcileKFM(t, r)

	if res.RequeueAfter != waitForRejoinRequeue {
		t.Fatalf("RequeueAfter = %v, want %v: the stale failure must not trigger a retry", res.RequeueAfter, waitForRejoinRequeue)
	}
	if len(fc.Reboots) != 1 {
		t.Fatalf("expected the stale reboot failure to be ignored, got %+v", fc.Reboots)
	}
	if getKFM(t, c).Annotations[rebootCommandIDAnnotation] != fakeRebootCommandID {
		t.Fatalf("expected the queued reboot's ID to be recorded, got %v", getKFM(t, c).Annotations)
	}
}

// A reboot that the node accepted must not be re-issued just because its command
// never reached a terminal phase: the node dies mid-command, so the outcome the
// controller reads back is usually Running or Delivered, not Completed.
func TestMachineReconcile_DoesNotRetryARebootInFlight(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			// Online with no heartbeat past the reboot, so the reboot command is
			// read back and its non-terminal phase is what holds the retry off.
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{
				{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted},
				{ID: fakeRebootCommandID, Command: fleet.CommandReboot, Phase: fleet.CommandPhaseRunning},
			}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // reboot
	res := reconcileKFM(t, r)

	if res.RequeueAfter != waitForRejoinRequeue {
		t.Fatalf("RequeueAfter = %v, want %v: an in-flight reboot is just a wait", res.RequeueAfter, waitForRejoinRequeue)
	}
	if len(fc.Reboots) != 1 {
		t.Fatalf("expected exactly one reboot while the first is in flight, got %+v", fc.Reboots)
	}
	if getKFM(t, c).Annotations[rebootRequestedAtAnnotation] == "" {
		t.Fatalf("the reboot marker must survive a reconcile that is only waiting")
	}
}

// A node that is away cannot report on its own reboot, so a terminal phase on the
// reboot command while it is Offline did not come from the node: AuroraBoot's
// PUT /commands/:id/status is unscoped for an admin token, and a server-side
// expiry sweep would be unscoped too. Acting on it reboots a machine that is
// already coming back, which for an HA control plane bounces a freshly joined
// etcd member. The controller waits on the heartbeat instead.
func TestMachineReconcile_DoesNotRetryARebootWhileTheNodeIsAway(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
		GetNodeFunc: func(_ context.Context, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOffline}, nil
		},
		GetCommandsFunc: func(_ context.Context, _ string) ([]fleet.Command, error) {
			return []fleet.Command{
				{ID: fakeApplyCommandID, Command: fleet.CommandApplyCloudConfig, Phase: fleet.CommandPhaseCompleted},
				{
					ID: fakeRebootCommandID, Command: fleet.CommandReboot,
					Phase: fleet.CommandPhaseFailed, Result: "expired",
				},
			}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	reconcileKFM(t, r) // apply
	reconcileKFM(t, r) // reboot
	res := reconcileKFM(t, r)

	if res.RequeueAfter != waitForRejoinRequeue {
		t.Fatalf("RequeueAfter = %v, want %v: a reboot marked terminal while the node is away is still a wait", res.RequeueAfter, waitForRejoinRequeue)
	}
	if len(fc.Reboots) != 1 {
		t.Fatalf("expected no second reboot while the node is away, got %+v", fc.Reboots)
	}
	kfm := getKFM(t, c)
	if kfm.Annotations[rebootRequestedAtAnnotation] == "" || kfm.Annotations[rebootCommandIDAnnotation] != fakeRebootCommandID {
		t.Fatalf("the reboot markers must survive, otherwise the next pass queues a second reboot, got %v", kfm.Annotations)
	}
	ready := meta.FindStatusCondition(kfm.Status.Conditions, clusterv1.ReadyCondition)
	if ready == nil || ready.Reason != "WaitingForNodeRejoin" {
		t.Fatalf("expected the machine to still be waiting for the rejoin, got %+v", ready)
	}

	// It comes back on a new boot: the machine provisions, and the failed reboot
	// command it left behind never causes a retry.
	hb := time.Now().Add(time.Hour)
	fc.GetNodeFunc = func(_ context.Context, _ string) (*fleet.Node, error) {
		return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline, LastHeartbeat: &hb}, nil
	}
	reconcileKFM(t, r)
	if !ptr.Deref(getKFM(t, c).Status.Initialization.Provisioned, false) {
		t.Fatalf("expected the machine to provision once the node rejoined")
	}
	if len(fc.Reboots) != 1 {
		t.Fatalf("expected still exactly one reboot after the node rejoined, got %+v", fc.Reboots)
	}
}

// The claim key is recorded when the node is claimed, so the release can present
// it even after the object's UID has changed.
func TestMachineReconcile_RecordsTheClaimKey(t *testing.T) {
	fc := &fleet.FakeClient{
		ClaimFunc: func(_ context.Context, _, _ string) (*fleet.Node, error) {
			return &fleet.Node{ID: testNodeID, Hostname: testHostname, Phase: fleet.PhaseOnline}, nil
		},
	}
	r, c := newReconciler(t, fc, testFixture(true))

	reconcileKFM(t, r) // claim
	if got := getKFM(t, c).Annotations[claimKeyAnnotation]; got != "kfm-uid" {
		t.Fatalf("claim-key annotation = %q, want the UID the node was claimed with (kfm-uid)", got)
	}
}

// clusterctl move and a backup and restore recreate the object with a new UID.
// The node is still claimed under the original key, and AuroraBoot refuses a
// release with any other key, so the release must use the recorded one: with the
// current UID the delete retried a 409 forever and kept its finalizer.
func TestMachineReconcile_ReleasesWithTheRecordedKeyAfterAMove(t *testing.T) {
	fc := &fleet.FakeClient{
		ReleaseFunc: func(_ context.Context, _, claimKey string) (bool, error) {
			if claimKey != "uid-before-the-move" {
				return false, fleet.ClaimMismatchError()
			}
			return true, nil
		},
	}
	objs := deletingFixture()
	for _, o := range objs {
		if kfm, ok := o.(*infrav1.KairosFleetMachine); ok {
			// The object now has a different UID (kfm-uid) from the one it claimed with.
			kfm.Annotations[claimKeyAnnotation] = "uid-before-the-move"
		}
	}
	r, c := newReconciler(t, fc, objs)

	reconcileKFM(t, r)
	if len(fc.Releases) != 1 || fc.Releases[0].ClaimKey != "uid-before-the-move" {
		t.Fatalf("expected the release to use the recorded claim key, got %+v", fc.Releases)
	}
	assertKFMGone(t, c)
}

// A node held under a different key can never be released by this machine:
// another machine claimed it after it was released out of band, or this machine
// was claimed before its key was recorded and has since moved. Retrying cannot
// succeed, so deletion must finish instead of keeping the finalizer forever.
func TestMachineReconcile_FinishesDeletingWhenTheNodeIsClaimedByAnotherKey(t *testing.T) {
	fc := &fleet.FakeClient{
		ReleaseFunc: func(_ context.Context, _, _ string) (bool, error) {
			return false, fleet.ClaimMismatchError()
		},
	}
	r, c := newReconciler(t, fc, deletingFixture())

	reconcileKFM(t, r)
	assertKFMGone(t, c)
}

// Any other release failure may be transient, so it keeps the finalizer and is
// retried: giving up would leak the node's claim.
func TestMachineReconcile_KeepsTheFinalizerWhenReleaseFails(t *testing.T) {
	fc := &fleet.FakeClient{
		ReleaseFunc: func(_ context.Context, _, _ string) (bool, error) {
			return false, &fleet.APIError{StatusCode: http.StatusInternalServerError, ErrorMsg: "failed to release node"}
		},
	}
	r, c := newReconciler(t, fc, deletingFixture())

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: testNS, Name: "kfm"},
	}); err == nil {
		t.Fatal("expected the release failure to surface so the delete is retried")
	}
	if !controllerutil.ContainsFinalizer(getKFM(t, c), infrav1.KairosFleetMachineFinalizer) {
		t.Fatal("expected the finalizer to be kept so the claim is not leaked")
	}
}
