/*
Copyright 2025.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterSecretSpec defines the desired state of ClusterSecret
type ClusterSecretSpec struct {
	Type       corev1.SecretType       `json:"type,omitempty" yaml:"type,omitempty"`
	Immutable  *bool                   `json:"immutable,omitempty" yaml:"immutable,omitempty"`
	Data       map[string][]byte       `json:"data,omitempty" yaml:"data,omitempty"`
	Namespaces ClusterSecretNamespaces `json:"namespaces,omitempty" yaml:"namespaces,omitempty"`
}

type ClusterSecretNamespaces struct {
	Include ClusterSecretNamespaceRule `json:"include,omitempty" yaml:"include,omitempty"`
	Exclude ClusterSecretNamespaceRule `json:"exclude,omitempty" yaml:"exclude,omitempty"`
}

type ClusterSecretNamespaceRule struct {
	Names    []string              `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	Regexp   *string               `json:"regexp,omitempty" yaml:"regexp,omitempty"`
	Selector *metav1.LabelSelector `json:"selector,omitempty" yaml:"selector,omitempty"`
}

// ClusterSecretStatus defines the observed state of ClusterSecret
type ClusterSecretStatus struct {
	Namespaces []string           `json:"namespaces,omitempty" yaml:"namespaces,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// ClusterSecret is the Schema for the clustersecrets API
type ClusterSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterSecretSpec   `json:"spec,omitempty"`
	Status ClusterSecretStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterSecretList contains a list of ClusterSecret
type ClusterSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterSecret `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterSecret{}, &ClusterSecretList{})
}
