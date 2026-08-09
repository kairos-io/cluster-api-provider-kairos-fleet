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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// KairosFleetClusterFinalizer allows the controller to clean up resources
	// associated with a KairosFleetCluster before it is removed from the API server.
	KairosFleetClusterFinalizer = "kairosfleetcluster.infrastructure.cluster.x-k8s.io"
)

// AuroraBootConnection describes how to reach the AuroraBoot fleet manager. It is
// shared by every KairosFleetMachine in the cluster.
type AuroraBootConnection struct {
	// url is the base URL of the AuroraBoot fleet API, e.g. "https://auroraboot.example:8080".
	// +required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// adminTokenSecretRef references a Secret in the same namespace as the KairosFleetCluster
	// holding the AuroraBoot admin bearer token. The token is read from the Secret's "token"
	// data key and is never written to logs.
	// +required
	AdminTokenSecretRef LocalSecretReference `json:"adminTokenSecretRef"`
}

// LocalSecretReference references a Secret in the same namespace as the referencing object.
type LocalSecretReference struct {
	// name is the name of the referenced Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// KairosFleetClusterSpec defines the desired state of KairosFleetCluster.
type KairosFleetClusterSpec struct {
	// controlPlaneEndpoint represents the endpoint used to communicate with the control
	// plane. For a fleet cluster the endpoint is operator-supplied (for example a kube-vip
	// VIP or a managed load balancer); this provider does not allocate it. It may be set at
	// creation or filled in later. status.initialization.provisioned gates on it being set.
	// +optional
	ControlPlaneEndpoint *clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty"`

	// auroraboot describes the AuroraBoot fleet manager connection shared by all machines
	// belonging to this cluster.
	// +required
	AuroraBoot AuroraBootConnection `json:"auroraboot"`
}

// KairosFleetClusterInitializationStatus provides observations of the KairosFleetCluster
// initialization process.
type KairosFleetClusterInitializationStatus struct {
	// provisioned is true when the infrastructure cluster is fully provisioned. This is the
	// Cluster API v1beta2 InfraCluster readiness signal read by the core Cluster controller.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// KairosFleetClusterStatus defines the observed state of KairosFleetCluster.
type KairosFleetClusterStatus struct {
	// initialization provides observations of the KairosFleetCluster initialization process.
	// +optional
	Initialization KairosFleetClusterInitializationStatus `json:"initialization,omitempty"`

	// conditions represents the observations of a KairosFleetCluster's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// failureReason is a terminal, programmatic failure indication. It is set only when the
	// cluster has entered an unrecoverable state. (Deprecated in the CAPI v1beta2 contract
	// but still surfaced.)
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// failureMessage is a terminal, human-readable description of a failure. It is set only
	// when the cluster has entered an unrecoverable state.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=kairosfleetclusters,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Provisioned",type="boolean",JSONPath=".status.initialization.provisioned",description="Whether the infrastructure cluster is provisioned"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.controlPlaneEndpoint.host",description="Control plane endpoint host"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KairosFleetCluster is the Schema for the kairosfleetclusters API. It is the
// infrastructure side of a Cluster API Cluster backed by an AuroraBoot fleet.
type KairosFleetCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KairosFleetClusterSpec   `json:"spec,omitempty"`
	Status KairosFleetClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KairosFleetClusterList contains a list of KairosFleetCluster.
type KairosFleetClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KairosFleetCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KairosFleetCluster{}, &KairosFleetClusterList{})
}
