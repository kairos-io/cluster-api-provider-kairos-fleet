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
	// not re-apply it.
	cloudConfigAppliedAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/cloud-config-applied"

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

// resolveFleetClient builds a fleet.Client for the given KairosFleetCluster by reading
// the AuroraBoot admin token from the referenced Secret. The token is never logged.
func resolveFleetClient(ctx context.Context, c client.Client, factory FleetClientFactory, fleetCluster *infrav1.KairosFleetCluster) (fleet.Client, error) {
	conn := fleetCluster.Spec.AuroraBoot
	if conn.URL == "" {
		return nil, fmt.Errorf("KairosFleetCluster %s/%s has no auroraboot.url", fleetCluster.Namespace, fleetCluster.Name)
	}
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: fleetCluster.Namespace, Name: conn.AdminTokenSecretRef.Name}
	if err := c.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting AuroraBoot admin token Secret %s: %w", key, err)
	}
	token := string(secret.Data[adminTokenSecretKey])
	if token == "" {
		return nil, fmt.Errorf("AuroraBoot admin token Secret %s has no %q key", key, adminTokenSecretKey)
	}
	return factory(conn.URL, token), nil
}
