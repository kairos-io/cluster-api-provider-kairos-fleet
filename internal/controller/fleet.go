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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/kairos-io/cluster-api-provider-kairos-fleet/api/v1alpha1"
	"github.com/kairos-io/cluster-api-provider-kairos-fleet/internal/fleet"
)

const (
	// adminTokenSecretKey is the data key in the AuroraBoot admin-token Secret.
	adminTokenSecretKey = "token"

	// bootstrapDataSecretKey is the data key CAPI bootstrap providers write the
	// cloud-config userdata under.
	bootstrapDataSecretKey = "value"

	// cloudConfigAppliedAnnotation marks that the bootstrap cloud-config has been
	// handed to AuroraBoot for the claimed node, so a level-triggered reconcile does
	// not re-apply it. Its presence is signalled by cloudConfigAppliedValue.
	cloudConfigAppliedAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-applied"
	cloudConfigAppliedValue      = "true"

	// cloudConfigCommandIDAnnotation records the ID of the apply-cloud-config
	// command the controller queued, so its outcome is read from that command and
	// not from an older apply against the same node. AuroraBoot never prunes a
	// node's command history, so a failed first attempt would otherwise shadow
	// every later success.
	cloudConfigCommandIDAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-command-id"

	// rebootRequestedAtAnnotation records (RFC3339) when the controller issued the
	// reboot that applies the staged cloud-config, so rejoin can be detected as a
	// node heartbeat newer than this time.
	rebootRequestedAtAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/reboot-requested-at"

	// rebootCommandIDAnnotation records the ID of the reboot command the
	// controller queued, so a rejected reboot is read back from that command
	// rather than from an earlier reboot of the same node. Same reasoning as
	// cloudConfigCommandIDAnnotation.
	rebootCommandIDAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/reboot-command-id"

	// retryNotBeforeAnnotation records (RFC3339) the earliest time the controller
	// may queue a fresh apply-cloud-config or reboot after the node reported the
	// previous one failed. The pacing has to live on the object: clearing the
	// command markers to allow the retry is itself an update, and the watch on
	// KairosFleetMachine answers it with an immediate reconcile, so a RequeueAfter
	// alone never delayed anything. A node that refused a command at once was sent
	// a new one on every pass.
	retryNotBeforeAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/retry-not-before"

	// claimKeyAnnotation records the key the node was claimed with, so the release
	// presents the same key. The key is the KairosFleetMachine's UID at claim
	// time, and a UID does not survive clusterctl move or a backup and restore,
	// both of which recreate the object with a new one. A release with the new
	// UID no longer matches, AuroraBoot refuses it with 409 ClaimMismatch, and a
	// delete that retried that forever kept the finalizer and blocked scale-down
	// and Cluster deletion.
	claimKeyAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/claim-key"

	// providerIDPrefix is the scheme for KairosFleetMachine provider IDs. The node
	// identifier is the AuroraBoot node ID (see ADR 0001 §3).
	providerIDPrefix = "kairos-fleet://"
)

// FleetClientFactory builds a fleet.Client for an AuroraBoot base URL + admin token.
// It is a field on the reconcilers so tests can inject a fake.
type FleetClientFactory func(baseURL, token string) fleet.Client

// DefaultFleetClientFactory returns a real net/http-backed fleet client.
func DefaultFleetClientFactory(baseURL, token string) fleet.Client {
	return fleet.New(baseURL, token)
}

// errConnectionIncomplete marks a KairosFleetCluster whose AuroraBoot connection can
// never be resolved as written: no url, or an admin-token Secret carrying no token.
// Unlike a read error it does not recover on its own, so a caller that needs the
// client only for best-effort cleanup can stop waiting for it.
var errConnectionIncomplete = errors.New("AuroraBoot connection is incomplete")

// resolveFleetClient builds a fleet.Client for the given KairosFleetCluster by reading
// the AuroraBoot admin token from the referenced Secret. The token is never logged.
func resolveFleetClient(ctx context.Context, c client.Client, factory FleetClientFactory, fleetCluster *infrav1.KairosFleetCluster) (fleet.Client, error) {
	conn := fleetCluster.Spec.AuroraBoot
	if conn.URL == "" {
		return nil, fmt.Errorf("KairosFleetCluster %s/%s has no auroraboot.url: %w", fleetCluster.Namespace, fleetCluster.Name, errConnectionIncomplete)
	}
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: fleetCluster.Namespace, Name: conn.AdminTokenSecretRef.Name}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting AuroraBoot admin token Secret %s: %w", key, err)
	}
	token := string(secret.Data[adminTokenSecretKey])
	if token == "" {
		return nil, fmt.Errorf("AuroraBoot admin token Secret %s has no %q key: %w", key, adminTokenSecretKey, errConnectionIncomplete)
	}
	return factory(conn.URL, token), nil
}
