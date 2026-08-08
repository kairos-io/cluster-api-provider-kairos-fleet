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
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
)

// KairosFleetClusterReconciler reconciles a KairosFleetCluster object.
type KairosFleetClusterReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	FleetClientFactory FleetClientFactory
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kairosfleetclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile satisfies the Cluster API v1beta2 InfraCluster contract: it validates the
// AuroraBoot connection and sets status.initialization.provisioned once the
// control-plane endpoint is present.
func (r *KairosFleetClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := logf.FromContext(ctx)

	fleetCluster := &infrav1.KairosFleetCluster{}
	if err := r.Get(ctx, req.NamespacedName, fleetCluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve the owning CAPI Cluster (set via owner reference by the Cluster controller).
	cluster, err := util.GetOwnerCluster(ctx, r.Client, fleetCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster owner reference to be set")
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(fleetCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchHelper.Patch(ctx, fleetCluster); err != nil && reterr == nil {
			reterr = err
		}
	}()

	if annotations.IsPaused(cluster, fleetCluster) {
		log.Info("Reconciliation is paused")
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(fleetCluster, infrav1.KairosFleetClusterFinalizer) {
		controllerutil.AddFinalizer(fleetCluster, infrav1.KairosFleetClusterFinalizer)
	}

	if !fleetCluster.DeletionTimestamp.IsZero() {
		// The InfraCluster owns no external infrastructure of its own (per-machine
		// nodes are released by the KairosFleetMachine controller), so there is
		// nothing to tear down here.
		controllerutil.RemoveFinalizer(fleetCluster, infrav1.KairosFleetClusterFinalizer)
		return ctrl.Result{}, nil
	}

	// Validate the AuroraBoot connection (the admin-token Secret must exist).
	if _, err := resolveFleetClient(ctx, r.Client, r.fleetFactory(), fleetCluster); err != nil {
		log.Info("AuroraBoot connection not ready", "reason", err.Error())
		setCondition(&fleetCluster.Status.Conditions, fleetCluster.Generation,
			clusterv1.ReadyCondition, metav1.ConditionFalse, "AuroraBootConnectionNotReady",
			"Waiting for a valid AuroraBoot connection")
		fleetCluster.Status.Initialization.Provisioned = ptr.To(false)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// The control-plane endpoint is operator-supplied for a fleet cluster; gate
	// readiness on it being present (ADR 0001 §5).
	if fleetCluster.Spec.ControlPlaneEndpoint == nil || fleetCluster.Spec.ControlPlaneEndpoint.Host == "" {
		log.Info("Waiting for control plane endpoint")
		setCondition(&fleetCluster.Status.Conditions, fleetCluster.Generation,
			clusterv1.ReadyCondition, metav1.ConditionFalse, "WaitingForControlPlaneEndpoint",
			"Waiting for spec.controlPlaneEndpoint to be set")
		fleetCluster.Status.Initialization.Provisioned = ptr.To(false)
		return ctrl.Result{}, nil
	}

	fleetCluster.Status.Initialization.Provisioned = ptr.To(true)
	setCondition(&fleetCluster.Status.Conditions, fleetCluster.Generation,
		clusterv1.ReadyCondition, metav1.ConditionTrue, "Provisioned",
		"Infrastructure cluster is provisioned")
	return ctrl.Result{}, nil
}

func (r *KairosFleetClusterReconciler) fleetFactory() FleetClientFactory {
	if r.FleetClientFactory != nil {
		return r.FleetClientFactory
	}
	return DefaultFleetClientFactory
}

// SetupWithManager sets up the controller with the Manager.
func (r *KairosFleetClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KairosFleetCluster{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(
				util.ClusterToInfrastructureMapFunc(
					context.Background(), infrav1.GroupVersion.WithKind("KairosFleetCluster"), mgr.GetClient(), &infrav1.KairosFleetCluster{}),
			),
		).
		Complete(r)
}

// meta.SetStatusCondition wrapper that stamps ObservedGeneration.
func setCondition(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conds, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}
