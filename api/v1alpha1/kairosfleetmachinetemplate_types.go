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

// KairosFleetMachineTemplateResource describes the data needed to create a
// KairosFleetMachine from a template.
type KairosFleetMachineTemplateResource struct {
	// metadata is the standard object metadata stamped onto the created KairosFleetMachine.
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification stamped onto the created KairosFleetMachine.
	// +required
	Spec KairosFleetMachineSpec `json:"spec"`
}

// KairosFleetMachineTemplateSpec defines the desired state of KairosFleetMachineTemplate.
type KairosFleetMachineTemplateSpec struct {
	// template is the KairosFleetMachine resource stamped out for each machine in a
	// MachineDeployment or MachineSet.
	// +required
	Template KairosFleetMachineTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kairosfleetmachinetemplates,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Group",type="string",JSONPath=".spec.template.spec.group",description="Target AuroraBoot group"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KairosFleetMachineTemplate is the Schema for the kairosfleetmachinetemplates API. It is
// used by MachineDeployments and MachineSets to stamp out KairosFleetMachine resources.
type KairosFleetMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KairosFleetMachineTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// KairosFleetMachineTemplateList contains a list of KairosFleetMachineTemplate.
type KairosFleetMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KairosFleetMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KairosFleetMachineTemplate{}, &KairosFleetMachineTemplateList{})
}
