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
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	// retryCloudConfigRequeue paces re-queuing apply-cloud-config after the node
	// rejected or failed it. Long enough that a node whose phonehome policy will
	// never permit the command does not accumulate failures quickly, short enough
	// that fixing the policy visibly unblocks the machine.
	retryCloudConfigRequeue = 60 * time.Second

	// retryRebootRequeue paces re-issuing the reboot after the node rejected or
	// failed it, for the same reasons as retryCloudConfigRequeue: the reboot is a
	// separate phonehome command with a separate policy gate, so it can be
	// rejected on a node that accepted the apply.
	retryRebootRequeue = 60 * time.Second
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
		return ctrl.Result{}, r.reconcileDelete(ctx, fleetMachine, cluster)
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

	claimKey := claimKeyFor(fleetMachine)

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
		annotations.AddAnnotations(fleetMachine, map[string]string{
			infrav1.NodeIDAnnotation: node.ID,
			claimKeyAnnotation:       claimKey,
		})
		log.Info("Claimed AuroraBoot node", "nodeID", node.ID, "group", fleetMachine.Spec.Group)
		r.notReady(fleetMachine, "NodeClaimed", "Claimed an AuroraBoot node; applying bootstrap configuration")
		// Return without an explicit requeue: the deferred patch persists the node-id
		// annotation, and the watch on KairosFleetMachine re-triggers reconcile to
		// apply the config.
		return ctrl.Result{}, nil
	}

	// 3. Apply the bootstrap cloud-config (once) — passed through unmodified.
	if fleetMachine.Annotations[cloudConfigAppliedAnnotation] != cloudConfigAppliedValue {
		if wait := retryWait(fleetMachine, time.Now()); wait > 0 {
			log.Info("Waiting before queuing a fresh apply-cloud-config after a failed one", "nodeID", nodeID, "retryIn", wait.Round(time.Second))
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		delete(fleetMachine.Annotations, retryNotBeforeAnnotation)
		data, err := r.bootstrapData(ctx, machine)
		if err != nil {
			return ctrl.Result{}, err
		}
		cmd, err := fc.ApplyCloudConfig(ctx, nodeID, data)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("applying cloud-config to node %s: %w", nodeID, err)
		}
		applied := map[string]string{cloudConfigAppliedAnnotation: cloudConfigAppliedValue}
		if cmd != nil && cmd.ID != "" {
			applied[cloudConfigCommandIDAnnotation] = cmd.ID
		}
		annotations.AddAnnotations(fleetMachine, applied)
		log.Info("Applied bootstrap cloud-config", "nodeID", nodeID)
		r.notReady(fleetMachine, "ApplyingCloudConfig", "Bootstrap cloud-config applied; waiting for the node to reboot and rejoin")
		return ctrl.Result{RequeueAfter: waitForRejoinRequeue}, nil
	}

	// 4. Once the apply-cloud-config command has completed, reboot the node so it
	// processes the staged /oem config (apply-cloud-config writes the file but does
	// not reboot).
	if fleetMachine.Annotations[rebootRequestedAtAnnotation] == "" {
		applied, failed, failMsg := r.commandState(ctx, fc, nodeID, fleetMachine.Annotations[cloudConfigCommandIDAnnotation], fleet.CommandApplyCloudConfig)
		if failed {
			// A failed apply is recoverable, not terminal: the usual cause is a
			// node-side phonehome policy that does not permit apply-cloud-config,
			// which the operator fixes on the node. Drop the applied markers so the
			// next pass queues a fresh command, and requeue — marking the machine
			// failed here would strand it even once the node is fixed.
			r.notReady(fleetMachine, "CloudConfigFailed", fmt.Sprintf("apply-cloud-config failed on node %s: %s", nodeID, failMsg))
			// Clear a terminal failure left by an earlier build, which marked this
			// same state failed; without this an upgraded machine stays failed.
			fleetMachine.Status.FailureReason = nil
			fleetMachine.Status.FailureMessage = nil
			delete(fleetMachine.Annotations, cloudConfigAppliedAnnotation)
			delete(fleetMachine.Annotations, cloudConfigCommandIDAnnotation)
			openRetryWindow(fleetMachine, time.Now(), retryCloudConfigRequeue)
			log.Info("apply-cloud-config failed; retrying with a fresh command", "nodeID", nodeID)
			return ctrl.Result{RequeueAfter: retryCloudConfigRequeue}, nil
		}
		if !applied {
			log.Info("Waiting for apply-cloud-config to complete on node", "nodeID", nodeID)
			r.notReady(fleetMachine, "ApplyingCloudConfig", "Waiting for the node to write the bootstrap cloud-config")
			return ctrl.Result{RequeueAfter: waitForRejoinRequeue}, nil
		}
		if wait := retryWait(fleetMachine, time.Now()); wait > 0 {
			log.Info("Waiting before queuing a fresh reboot after a failed one", "nodeID", nodeID, "retryIn", wait.Round(time.Second))
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		delete(fleetMachine.Annotations, retryNotBeforeAnnotation)
		cmd, err := fc.Reboot(ctx, nodeID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("rebooting node %s: %w", nodeID, err)
		}
		requested := map[string]string{rebootRequestedAtAnnotation: time.Now().UTC().Format(time.RFC3339)}
		if cmd != nil && cmd.ID != "" {
			requested[rebootCommandIDAnnotation] = cmd.ID
		}
		annotations.AddAnnotations(fleetMachine, requested)
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
		// The reboot is a phonehome command in its own right, gated by the node's
		// own allowed_commands policy, so a node that accepted apply-cloud-config
		// can still reject the reboot. Read that command back before settling into
		// the rejoin wait: without this the machine waits on a boot that will never
		// happen, and the node's reason for refusing is never surfaced. Only a
		// terminal failure counts - a reboot the node accepted usually never
		// reports Completed, because the node goes down mid-command.
		//
		// Only ask while the node is Online. A node that refuses the reboot stays
		// up to refuse it, so the case this retry exists for is unaffected. A node
		// that is Offline is plausibly performing the reboot, and it cannot report
		// anything while it is down, so a terminal phase on its reboot command was
		// not written by the node: AuroraBoot's admin status endpoint is unscoped,
		// and an expiry sweep added later would be too. Retrying on that reboots a
		// machine already on its way back, and for an HA control plane it bounces
		// a freshly joined etcd member. Waiting instead is what the heartbeat
		// check above already does.
		if failed, failMsg := r.rebootFailed(ctx, fc, fleetMachine, node, nodeID); failed {
			r.notReady(fleetMachine, "RebootFailed", fmt.Sprintf("reboot failed on node %s: %s", nodeID, failMsg))
			delete(fleetMachine.Annotations, rebootRequestedAtAnnotation)
			delete(fleetMachine.Annotations, rebootCommandIDAnnotation)
			openRetryWindow(fleetMachine, time.Now(), retryRebootRequeue)
			log.Info("Reboot failed; retrying with a fresh command", "nodeID", nodeID, "result", failMsg)
			return ctrl.Result{RequeueAfter: retryRebootRequeue}, nil
		}
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

// reconcileDelete releases the claimed node and removes the finalizer. It returns
// only an error: the delete path either finishes or fails, and a failure is retried
// by the controller's own backoff, so no timed requeue is left on it.
func (r *KairosFleetMachineReconciler) reconcileDelete(ctx context.Context, fleetMachine *infrav1.KairosFleetMachine, cluster *clusterv1.Cluster) error {
	log := logf.FromContext(ctx)

	nodeID := fleetMachine.Annotations[infrav1.NodeIDAnnotation]
	if nodeID != "" {
		fc, err := r.fleetClientForDelete(ctx, cluster)
		switch {
		case err != nil && !isPermanentlyUnresolvable(err):
			// A transient read error. Retry: giving up here would remove the
			// finalizer and leak the node's claim, so the group never gets it back.
			return fmt.Errorf("resolving AuroraBoot connection to release node %s: %w", nodeID, err)
		case err != nil:
			// The KairosFleetCluster or its admin-token Secret is gone for good, so
			// there is no way left to reach AuroraBoot. Nothing about that recovers
			// while the machine sits in Terminating; log and let deletion proceed.
			log.Info("Cannot resolve AuroraBoot connection on delete; removing finalizer without release", "err", err.Error())
		default:
			claimKey := claimKeyFor(fleetMachine)
			_, err := fc.Release(ctx, nodeID, claimKey)
			switch {
			case err == nil:
				log.Info("Released AuroraBoot node", "nodeID", nodeID)
			case fleet.IsNotFound(err):
				log.Info("AuroraBoot node is already gone; nothing to release", "nodeID", nodeID)
			case fleet.IsClaimMismatch(err):
				// The node is held under a different key, so this machine can never
				// release it, and retrying would only keep the finalizer forever.
				// Either another machine claimed the node after it was released out
				// of band, or this machine was claimed before its key was recorded
				// and has since been moved to a new UID. Let deletion finish, and say
				// which node so an operator can return it to its group if nothing
				// else owns it.
				log.Info("AuroraBoot node is claimed by a different key; removing the finalizer without a release. "+
					"If no other machine owns the node, release it in AuroraBoot to return it to its group",
					"nodeID", nodeID, "claimKey", claimKey)
			default:
				return fmt.Errorf("releasing node %s: %w", nodeID, err)
			}
		}
	}

	controllerutil.RemoveFinalizer(fleetMachine, infrav1.KairosFleetMachineFinalizer)
	return nil
}

// rebootFailed reports whether the reboot this controller queued failed, and the
// failure message, asking only while the node is still Online. See the call site
// for why a terminal phase reported while the node is away is not the node's own
// verdict on the reboot.
func (r *KairosFleetMachineReconciler) rebootFailed(ctx context.Context, fc fleet.Client, fleetMachine *infrav1.KairosFleetMachine, node *fleet.Node, nodeID string) (bool, string) {
	if node.Phase != fleet.PhaseOnline {
		return false, ""
	}
	_, failed, failMsg := r.commandState(ctx, fc, nodeID, fleetMachine.Annotations[rebootCommandIDAnnotation], fleet.CommandReboot)
	return failed, failMsg
}

// openRetryWindow records when a fresh command may next be queued, after the
// node reported the previous one failed. See retryNotBeforeAnnotation for why
// this cannot be a RequeueAfter alone.
func openRetryWindow(fleetMachine *infrav1.KairosFleetMachine, now time.Time, after time.Duration) {
	annotations.AddAnnotations(fleetMachine, map[string]string{
		retryNotBeforeAnnotation: now.Add(after).UTC().Format(time.RFC3339),
	})
}

// retryWait reports how long the controller must still wait before queuing a
// fresh command, or zero when no retry window is open. An unparseable value is
// treated as expired rather than holding the machine forever.
func retryWait(fleetMachine *infrav1.KairosFleetMachine, now time.Time) time.Duration {
	v := fleetMachine.Annotations[retryNotBeforeAnnotation]
	if v == "" {
		return 0
	}
	notBefore, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0
	}
	if wait := notBefore.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

// claimKeyFor returns the key this machine claims and releases its node with:
// the one recorded at claim time when there is one, otherwise the object's UID,
// which is the key a first claim uses and the one a machine claimed by an earlier
// build was claimed with.
func claimKeyFor(fleetMachine *infrav1.KairosFleetMachine) string {
	if key := fleetMachine.Annotations[claimKeyAnnotation]; key != "" {
		return key
	}
	return string(fleetMachine.UID)
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

// commandState reports whether the command named by cmdID completed and, if it
// failed, the failure message. name is the command kind (apply-cloud-config,
// reboot) it belongs to, used only by the no-id fallback.
//
// AuroraBoot retains every command ever queued for a node, so the outcome must be
// read from the command this controller queued. When cmdID is empty — a machine
// first reconciled by a build that did not record it — fall back to the most
// recently created command of that kind. Matching the first entry instead would
// let one early failure shadow every later success, permanently.
func (r *KairosFleetMachineReconciler) commandState(ctx context.Context, fc fleet.Client, nodeID, cmdID, name string) (completed, failed bool, failMsg string) {
	cmds, err := fc.GetCommands(ctx, nodeID)
	if err != nil {
		// Treat a transient list error as "not yet completed"; the caller requeues.
		return false, false, ""
	}

	var latest *fleet.Command
	for i := range cmds {
		cmd := &cmds[i]
		if cmdID != "" {
			if cmd.ID == cmdID {
				latest = cmd
				break
			}
			continue
		}
		if cmd.Command != name {
			continue
		}
		if latest == nil || newerCommand(cmd, latest) {
			latest = cmd
		}
	}
	if latest == nil {
		return false, false, ""
	}

	switch latest.Phase {
	case fleet.CommandPhaseCompleted:
		return true, false, ""
	case fleet.CommandPhaseFailed, fleet.CommandPhaseExpired:
		return false, true, latest.Result
	}
	return false, false, ""
}

// newerCommand reports whether a was created after b. A command AuroraBoot did not
// timestamp counts as the newer one, so a response with no createdAt at all still
// resolves to the last entry in the list rather than the first.
func newerCommand(a, b *fleet.Command) bool {
	if a.CreatedAt == nil {
		return true
	}
	if b.CreatedAt == nil {
		return false
	}
	return a.CreatedAt.After(*b.CreatedAt)
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

// fleetClientForDelete resolves a fleet.Client for the release-on-delete path. It
// reports every failure as an error, where fleetClientFor folds a missing
// KairosFleetCluster and an unusable connection into "wait and retry". On delete
// that distinction matters: the InfraCluster removes its own finalizer as soon as it
// is deleted (it owns no external infrastructure), so it and its Secret can already
// be gone while machines are still terminating, and waiting for them is waiting
// forever.
func (r *KairosFleetMachineReconciler) fleetClientForDelete(ctx context.Context, cluster *clusterv1.Cluster) (fleet.Client, error) {
	fleetCluster := &infrav1.KairosFleetCluster{}
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}
	if err := r.Get(ctx, key, fleetCluster); err != nil {
		return nil, fmt.Errorf("getting KairosFleetCluster %s: %w", key, err)
	}
	return resolveFleetClient(ctx, r.Client, r.fleetFactory(), fleetCluster)
}

// isPermanentlyUnresolvable reports whether an AuroraBoot connection failure will
// still be a failure on every later attempt: the KairosFleetCluster or the Secret
// does not exist, or the connection is missing its url or token. Anything else is a
// read failure that can succeed on a retry.
func isPermanentlyUnresolvable(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, errConnectionIncomplete)
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

// machineAddressMaxLength is the upper bound Cluster API puts on
// MachineAddress.address.
const machineAddressMaxLength = 256

// clusterAPIAddressTypes is the enum Cluster API constrains MachineAddress.type to.
// AuroraBoot stores the type a node reports as a free-form string so a new type
// needs no server change, so the provider matches it against this set rather than
// passing it through: an address the apiserver would reject takes the whole status
// update with it, including the providerID and the Ready condition.
var clusterAPIAddressTypes = map[string]clusterv1.MachineAddressType{
	string(clusterv1.MachineHostName):    clusterv1.MachineHostName,
	string(clusterv1.MachineInternalIP):  clusterv1.MachineInternalIP,
	string(clusterv1.MachineExternalIP):  clusterv1.MachineExternalIP,
	string(clusterv1.MachineInternalDNS): clusterv1.MachineInternalDNS,
	string(clusterv1.MachineExternalDNS): clusterv1.MachineExternalDNS,
}

// addressesFromNode maps the addresses a node reported to AuroraBoot onto Cluster
// API machine addresses, led by the node's hostname. The hostname is always
// available; the reported list is optional, because an agent that does not collect
// addresses, or one older than the field, sends none.
//
// Addresses are kept in the order the node reported them: they describe its NICs
// and the agent decides that order. An address whose type is not in Cluster API's
// enum, or whose value is empty or longer than Cluster API accepts, is dropped
// rather than passed through, and an exact repeat is reported once.
func addressesFromNode(node *fleet.Node) []clusterv1.MachineAddress {
	var out []clusterv1.MachineAddress
	seen := map[clusterv1.MachineAddress]bool{}

	add := func(addr clusterv1.MachineAddress) {
		if addr.Address == "" || len(addr.Address) > machineAddressMaxLength || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, addr)
	}

	add(clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: node.Hostname})
	for _, a := range node.Addresses {
		typ, ok := clusterAPIAddressTypes[a.Type]
		if !ok {
			continue
		}
		add(clusterv1.MachineAddress{Type: typ, Address: a.Address})
	}
	return out
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
