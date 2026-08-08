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

// KairosFleetClusterTemplateResource describes the data needed to create a
// KairosFleetCluster from a template.
type KairosFleetClusterTemplateResource struct {
	// metadata is the standard object metadata stamped onto the created KairosFleetCluster.
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification stamped onto the created KairosFleetCluster.
	// +required
	Spec KairosFleetClusterSpec `json:"spec"`
}

// KairosFleetClusterTemplateSpec defines the desired state of KairosFleetClusterTemplate.
type KairosFleetClusterTemplateSpec struct {
	// template is the KairosFleetCluster resource stamped out for each cluster.
	// +required
	Template KairosFleetClusterTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kairosfleetclustertemplates,scope=Namespaced,categories=cluster-api
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KairosFleetClusterTemplate is the Schema for the kairosfleetclustertemplates API. It is
// used by ClusterClass to stamp out KairosFleetCluster resources.
type KairosFleetClusterTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KairosFleetClusterTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// KairosFleetClusterTemplateList contains a list of KairosFleetClusterTemplate.
type KairosFleetClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KairosFleetClusterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KairosFleetClusterTemplate{}, &KairosFleetClusterTemplateList{})
}
