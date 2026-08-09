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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/kairos-io/cluster-api-provider-kairos-fleet/api/v1alpha1"
	"github.com/kairos-io/cluster-api-provider-kairos-fleet/internal/fleet"
)

// Requeue intervals for the staged claim -> apply -> rejoin lifecycle.
const (
	waitForCapacityRequeue = 30 * time.Second
	waitForRejoinRequeue   = 15 * time.Second
)

// KairosFleetMachineReconciler reconciles a KairosFleetMachine object.
type KairosFleetMachineReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	FleetClientFactory FleetClientFactory
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetmachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile drives the KairosFleetMachine state machine: wait for bootstrap data,
// claim an AuroraBoot node, apply the bootstrap cloud-config, wait for the node to
// rejoin, then set providerID/addresses and mark it provisioned. On delete it releases
// the claimed node before removing the finalizer.
func (r *KairosFleetMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := logf.FromContext(ctx)

	fleetMachine := &infrav1.KairosFleetMachine{}
	if err := r.Get(ctx, req.NamespacedName, fleetMachine); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve the owning CAPI Machine (set via owner reference by the Machine controller).
	machine, err := util.GetOwnerMachine(ctx, r.Client, fleetMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Waiting for Machine owner reference to be set")
		return ctrl.Result{}, nil
	}

	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		log.Info("KairosFleetMachine owner Machine is missing cluster label", "err", err.Error())
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(fleetMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchHelper.Patch(ctx, fleetMachine); err != nil && reterr == nil {
			reterr = err
		}
	}()

	// The InfraMachine is owned by the CAPI Machine, not the control plane.
	if err := controllerutil.SetControllerReference(machine, fleetMachine, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	if annotations.IsPaused(cluster, fleetMachine) {
		log.Info("Reconciliation is paused")
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(fleetMachine, infrav1.KairosFleetMachineFinalizer) {
		controllerutil.AddFinalizer(fleetMachine, infrav1.KairosFleetMachineFinalizer)
	}

	if !fleetMachine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, fleetMachine, cluster)
	}
	return r.reconcileNormal(ctx, fleetMachine, machine, cluster)
}

func (r *KairosFleetMachineReconciler) reconcileNormal(ctx context.Context, fleetMachine *infrav1.KairosFleetMachine, machine *clusterv1.Machine, cluster *clusterv1.Cluster) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Wait for the bootstrap provider to publish the cloud-config data secret.
	if machine.Spec.Bootstrap.DataSecretName == nil {
		log.Info("Waiting for bootstrap data secret")
		r.notReady(fleetMachine, "WaitingForBootstrapData", "Waiting for the bootstrap data secret to be available")
		return ctrl.Result{}, nil
	}

	fc, res, err := r.fleetClientFor(ctx, cluster)
	if err != nil || fc == nil {
		r.notReady(fleetMachine, "WaitingForClusterInfrastructure", "Waiting for a valid AuroraBoot connection on the KairosFleetCluster")
		return res, err
	}

	claimKey := string(fleetMachine.UID)

	// 2. Claim a node from the group (idempotent on claimKey).
	nodeID := fleetMachine.Annotations[infrav1.NodeIDAnnotation]
	if nodeID == "" {
		// AuroraBoot's claim endpoint keys on the group's ID, but spec.group is the
		// human-friendly group name; resolve it first.
		groupID, err := fc.ResolveGroupID(ctx, fleetMachine.Spec.Group)
		if err != nil {
			if fleet.IsNotFound(err) {
				log.Info("AuroraBoot group not found, waiting", "group", fleetMachine.Spec.Group)
				r.notReady(fleetMachine, "GroupNotFound", fmt.Sprintf("spec.group %q does not match any AuroraBoot group", fleetMachine.Spec.Group))
				return ctrl.Result{RequeueAfter: waitForCapacityRequeue}, nil
			}
			return ctrl.Result{}, fmt.Errorf("resolving group %q: %w", fleetMachine.Spec.Group, err)
		}

		node, err := fc.Claim(ctx, groupID, claimKey)
		if err != nil {
			if fleet.IsNoCapacity(err) {
				log.Info("No capacity in group, waiting", "group", fleetMachine.Spec.Group)
				r.notReady(fleetMachine, "WaitingForCapacity", fmt.Sprintf("Waiting for an available node in group %q", fleetMachine.Spec.Group))
				return ctrl.Result{RequeueAfter: waitForCapacityRequeue}, nil
			}
			return ctrl.Result{}, fmt.Errorf("claiming node from group %q: %w", fleetMachine.Spec.Group, err)
		}
		annotations.AddAnnotations(fleetMachine, map[string]string{infrav1.NodeIDAnnotation: node.ID})
		log.Info("Claimed AuroraBoot node", "nodeID", node.ID, "group", fleetMachine.Spec.Group)
		r.notReady(fleetMachine, "NodeClaimed", "Claimed an AuroraBoot node; applying bootstrap configuration")
		// Return without an explicit requeue: the deferred patch persists the node-id
		// annotation, and the watch on KairosFleetMachine re-triggers reconcile to
		// apply the config.
		return ctrl.Result{}, nil
	}

	// 3. Apply the bootstrap cloud-config (once) — passed through unmodified.
	if fleetMachine.Annotations[cloudConfigAppliedAnnotation] != cloudConfigAppliedValue {
		data, err := r.bootstrapData(ctx, machine)
		if err != nil {
			return ctrl.Result{}, err
		}
		if _, err := fc.ApplyCloudConfig(ctx, nodeID, data); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying cloud-config to node %s: %w", nodeID, err)
		}
		annotations.AddAnnotations(fleetMachine, map[string]string{cloudConfigAppliedAnnotation: cloudConfigAppliedValue})
		log.Info("Applied bootstrap cloud-config", "nodeID", nodeID)
		r.notReady(fleetMachine, "ApplyingCloudConfig", "Bootstrap cloud-config applied; waiting for the node to reboot and rejoin")
		return ctrl.Result{RequeueAfter: waitForRejoinRequeue}, nil
	}

	// 4. Once the apply-cloud-config command has completed, reboot the node so it
	// processes the staged /oem config (apply-cloud-config writes the file but does
	// not reboot).
	if fleetMachine.Annotations[rebootRequestedAtAnnotation] == "" {
		applied, failed, failMsg := r.applyState(ctx, fc, nodeID)
		if failed {
			r.fail(fleetMachine, "CloudConfigFailed", fmt.Sprintf("apply-cloud-config failed on node %s: %s", nodeID, failMsg))
			return ctrl.Result{}, nil
		}
		if !applied {
			log.Info("Waiting for apply-cloud-config to complete on node", "nodeID", nodeID)
			r.notReady(fleetMachine, "ApplyingCloudConfig", "Waiting for the node to write the bootstrap cloud-config")
			return ctrl.Result{RequeueAfter: waitForRejoinRequeue}, nil
		}
		if _, err := fc.Reboot(ctx, nodeID); err != nil {
			return ctrl.Result{}, fmt.Errorf("rebooting node %s: %w", nodeID, err)
		}
		annotations.AddAnnotations(fleetMachine, map[string]string{
			rebootRequestedAtAnnotation: time.Now().UTC().Format(time.RFC3339),
		})
		log.Info("Requested node reboot to apply cloud-config", "nodeID", nodeID)
		r.notReady(fleetMachine, "Rebooting", "Rebooting the node to apply its bootstrap configuration")
		return ctrl.Result{RequeueAfter: waitForRejoinRequeue}, nil
	}

	// 5. Wait for the node to reboot and rejoin: Online with a heartbeat newer than
	// the reboot request.
	node, err := fc.GetNode(ctx, nodeID)
	if err != nil {
		if fleet.IsNotFound(err) {
			r.fail(fleetMachine, "NodeMissing", fmt.Sprintf("Claimed AuroraBoot node %s no longer exists", nodeID))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting node %s: %w", nodeID, err)
	}
	if !r.rejoinedAfterReboot(fleetMachine, node) {
		log.Info("Waiting for node to rejoin after reboot", "nodeID", nodeID, "phase", node.Phase)
		r.notReady(fleetMachine, "WaitingForNodeRejoin", "Waiting for the node to reboot and report Online")
		return ctrl.Result{RequeueAfter: waitForRejoinRequeue}, nil
	}

	// 6. Provisioned: publish providerID + addresses and mark ready.
	providerID := providerIDPrefix + node.ID
	fleetMachine.Spec.ProviderID = ptr.To(providerID)
	fleetMachine.Status.Addresses = addressesFromNode(node)
	fleetMachine.Status.Initialization.Provisioned = ptr.To(true)
	meta.SetStatusCondition(&fleetMachine.Status.Conditions, metav1.Condition{
		Type:               clusterv1.ReadyCondition,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: fleetMachine.Generation,
		Reason:             "Provisioned",
		Message:            "Node is claimed, configured and Online",
	})
	log.Info("KairosFleetMachine provisioned", "nodeID", node.ID, "providerID", providerID)
	return ctrl.Result{}, nil
}

func (r *KairosFleetMachineReconciler) reconcileDelete(ctx context.Context, fleetMachine *infrav1.KairosFleetMachine, cluster *clusterv1.Cluster) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nodeID := fleetMachine.Annotations[infrav1.NodeIDAnnotation]
	if nodeID != "" {
		fc, res, err := r.fleetClientFor(ctx, cluster)
		switch {
		case err != nil:
			// Cannot resolve the AuroraBoot connection (e.g. the KairosFleetCluster or
			// its Secret is already gone). Nothing more we can do to release the node;
			// log and let deletion proceed rather than blocking it forever.
			log.Info("Cannot resolve AuroraBoot connection on delete; releasing finalizer without release", "err", err.Error())
		case fc == nil:
			return res, nil
		default:
			claimKey := string(fleetMachine.UID)
			if _, err := fc.Release(ctx, nodeID, claimKey); err != nil && !fleet.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("releasing node %s: %w", nodeID, err)
			}
			log.Info("Released AuroraBoot node", "nodeID", nodeID)
		}
	}

	controllerutil.RemoveFinalizer(fleetMachine, infrav1.KairosFleetMachineFinalizer)
	return ctrl.Result{}, nil
}

// rejoinedAfterReboot reports whether the node has come back Online after the reboot
// the controller requested — detected as a heartbeat newer than the recorded reboot
// time, so a level-triggered reconcile need not catch the transient Offline.
func (r *KairosFleetMachineReconciler) rejoinedAfterReboot(fleetMachine *infrav1.KairosFleetMachine, node *fleet.Node) bool {
	if node.Phase != fleet.PhaseOnline || node.LastHeartbeat == nil {
		return false
	}
	rebootedAt, err := time.Parse(time.RFC3339, fleetMachine.Annotations[rebootRequestedAtAnnotation])
	if err != nil {
		return false
	}
	return node.LastHeartbeat.After(rebootedAt)
}

// applyState reports whether the apply-cloud-config command completed and, if it
// failed, the failure message.
func (r *KairosFleetMachineReconciler) applyState(ctx context.Context, fc fleet.Client, nodeID string) (completed, failed bool, failMsg string) {
	cmds, err := fc.GetCommands(ctx, nodeID)
	if err != nil {
		// Treat a transient list error as "not yet completed"; the caller requeues.
		return false, false, ""
	}
	for i := range cmds {
		if cmds[i].Command != fleet.CommandApplyCloudConfig {
			continue
		}
		switch cmds[i].Phase {
		case fleet.CommandPhaseCompleted:
			return true, false, ""
		case fleet.CommandPhaseFailed, fleet.CommandPhaseExpired:
			return false, true, cmds[i].Result
		}
	}
	return false, false, ""
}

// fleetClientFor resolves a fleet.Client from the cluster's KairosFleetCluster. It
// returns (nil, requeue-result, nil) when the InfraCluster is not yet available.
func (r *KairosFleetMachineReconciler) fleetClientFor(ctx context.Context, cluster *clusterv1.Cluster) (fleet.Client, ctrl.Result, error) {
	fleetCluster := &infrav1.KairosFleetCluster{}
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
	if err := r.Get(ctx, key, fleetCluster); err != nil {
		return nil, ctrl.Result{RequeueAfter: waitForRejoinRequeue}, client.IgnoreNotFound(err)
	}
	fc, err := resolveFleetClient(ctx, r.Client, r.fleetFactory(), fleetCluster)
	if err != nil {
		return nil, ctrl.Result{RequeueAfter: waitForCapacityRequeue}, nil
	}
	return fc, ctrl.Result{}, nil
}

func (r *KairosFleetMachineReconciler) bootstrapData(ctx context.Context, machine *clusterv1.Machine) (string, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}
	if err := r.Get(ctx, key, secret); err != nil {
		return "", fmt.Errorf("getting bootstrap data secret %s: %w", key, err)
	}
	data, ok := secret.Data[bootstrapDataSecretKey]
	if !ok {
		return "", fmt.Errorf("bootstrap data secret %s has no %q key", key, bootstrapDataSecretKey)
	}
	return string(data), nil
}

func (r *KairosFleetMachineReconciler) notReady(fleetMachine *infrav1.KairosFleetMachine, reason, message string) {
	fleetMachine.Status.Initialization.Provisioned = ptr.To(false)
	meta.SetStatusCondition(&fleetMachine.Status.Conditions, metav1.Condition{
		Type:               clusterv1.ReadyCondition,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: fleetMachine.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *KairosFleetMachineReconciler) fail(fleetMachine *infrav1.KairosFleetMachine, reason, message string) {
	fleetMachine.Status.FailureReason = ptr.To(reason)
	fleetMachine.Status.FailureMessage = ptr.To(message)
	r.notReady(fleetMachine, reason, message)
}

func (r *KairosFleetMachineReconciler) fleetFactory() FleetClientFactory {
	if r.FleetClientFactory != nil {
		return r.FleetClientFactory
	}
	return DefaultFleetClientFactory
}

// addressesFromNode derives machine addresses from the node's reported identity.
// AuroraBoot does not expose structured addresses; the hostname is surfaced as a
// Hostname address (see ADR 0001 §6).
func addressesFromNode(node *fleet.Node) []clusterv1.MachineAddress {
	if node.Hostname == "" {
		return nil
	}
	return []clusterv1.MachineAddress{
		{Type: clusterv1.MachineHostName, Address: node.Hostname},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *KairosFleetMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KairosFleetMachine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(
				util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("KairosFleetMachine")),
			),
		).
		Complete(r)
}
