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
	// KairosFleetMachineFinalizer allows the controller to release the claimed AuroraBoot
	// node before the KairosFleetMachine is removed from the API server.
	KairosFleetMachineFinalizer = "kairosfleetmachine.infrastructure.cluster.x-k8s.io"

	// NodeIDAnnotation records the AuroraBoot node ID of the node claimed for this machine.
	// It is the source of both spec.providerID and the release call on delete, and is set by
	// the controller once the claim succeeds. It is an annotation (not spec) because the user
	// does not choose it, and (not status) because it must survive independently of status
	// rebuilds and is operationally visible.
	NodeIDAnnotation = "kairos-fleet.infrastructure.cluster.x-k8s.io/node-id"
)

// KairosFleetMachineSpec defines the desired state of KairosFleetMachine.
type KairosFleetMachineSpec struct {
	// group is the AuroraBoot group to claim a node from. It accepts either the group's
	// name or its ID; the controller resolves a name to the group's ID before claiming.
	// The group must already exist in AuroraBoot and hold enrolled, unclaimed nodes. A
	// group that does not resolve is a transient "GroupNotFound" wait, and a resolved
	// group with no unclaimed nodes is a transient "waiting for capacity" wait; neither
	// is a failure.
	// +required
	// +kubebuilder:validation:MinLength=1
	Group string `json:"group"`

	// providerID is the identifier for the claimed AuroraBoot node, in the form
	// "kairos-fleet://<id>". The controller sets it once the node has rejoined as Online
	// after the reboot that applies its bootstrap cloud-config, at the same time it sets
	// status.initialization.provisioned. It is not validated or protected against edits:
	// users must not set or change it.
	// +optional
	ProviderID *string `json:"providerID,omitempty"`
}

// KairosFleetMachineInitializationStatus provides observations of the KairosFleetMachine
// initialization process.
type KairosFleetMachineInitializationStatus struct {
	// provisioned is true once a node has been claimed, its bootstrap cloud-config applied,
	// and the node has been rebooted and has rejoined AuroraBoot as Online. This is the
	// Cluster API v1beta2 InfraMachine initialization signal read by the core Machine
	// controller. Once true it stays true; the Ready condition reports the current state.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// KairosFleetMachineStatus defines the observed state of KairosFleetMachine.
type KairosFleetMachineStatus struct {
	// initialization provides observations of the KairosFleetMachine initialization process.
	// +optional
	Initialization KairosFleetMachineInitializationStatus `json:"initialization,omitempty"`

	// addresses is the list of addresses reported for the claimed node, mirrored from
	// AuroraBoot's reported node addresses (for example InternalIP and Hostname). The
	// controller refreshes it each time the node is observed rejoined, not while the
	// node is claimed but still waiting to rejoin.
	// +optional
	// +listType=atomic
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// conditions represents the observations of a KairosFleetMachine's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// failureReason is a programmatic failure indication. The controller sets it when the
	// claimed AuroraBoot node no longer exists (reason "NodeMissing"); the Ready condition
	// carries the same reason. (Deprecated in the CAPI v1beta2 contract but still
	// surfaced.)
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// failureMessage is a human-readable description of the failure named by
	// failureReason, and is set together with it.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=kairosfleetmachines,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Provisioned",type="boolean",JSONPath=".status.initialization.provisioned",description="Whether the infrastructure machine is provisioned"
// +kubebuilder:printcolumn:name="Group",type="string",JSONPath=".spec.group",description="Target AuroraBoot group"
// +kubebuilder:printcolumn:name="ProviderID",type="string",JSONPath=".spec.providerID",description="Claimed node provider ID"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KairosFleetMachine is the Schema for the kairosfleetmachines API. It is the
// infrastructure side of a Cluster API Machine, backed by an AuroraBoot node claimed
// from a group.
type KairosFleetMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KairosFleetMachineSpec   `json:"spec,omitempty"`
	Status KairosFleetMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KairosFleetMachineList contains a list of KairosFleetMachine.
type KairosFleetMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KairosFleetMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KairosFleetMachine{}, &KairosFleetMachineList{})
}
