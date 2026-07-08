/*
 * Copyright 2026 Red Hat, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OvsDpdkResourcePolicy defines the policy for advertising OVS bridges that the
// driver will expose as DRA devices.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type OvsDpdkResourcePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec OvsDpdkResourcePolicySpec `json:"spec"`
}

// OvsDpdkResourcePolicySpec defines the desired state of OvsDpdkResourcePolicy.
type OvsDpdkResourcePolicySpec struct {
	// NodeSelector restricts the nodes to which this policy applies.
	// If not set, the policy applies to all nodes.
	// +optional
	NodeSelector *corev1.NodeSelector `json:"nodeSelector,omitempty"`

	// Bridges is the list of OVS bridges exposed as DRA devices by this policy.
	// +kubebuilder:validation:MinItems=1
	Bridges []BridgeSpec `json:"bridges"`
}

// BridgeSpec defines a single OVS bridge to be exposed as a DRA device.
type BridgeSpec struct {
	// Name is the name of the OVS bridge.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// TopologyResource, if set, enables a Device Plugin that exposes the NUMA
	// topology of this bridge's DPDK uplinks as a schedulable resource.
	// The value is the resource name suffix; the full extended resource name
	// will be "ovsdpdk.k8snetworkplumbingwg.io/<suffix>" (e.g. "topology-br0"
	// becomes "ovsdpdk.k8snetworkplumbingwg.io/topology-br0").
	// Must be a valid DNS label: alphanumeric, dashes, underscores, or dots,
	// starting with an alphanumeric character (max 63 characters).
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9][-a-zA-Z0-9_.]*$`
	// +kubebuilder:validation:MaxLength=63
	TopologyResource string `json:"topologyResource,omitempty"`
}

// OvsDpdkResourcePolicyList contains a list of OvsDpdkResourcePolicy.
//
// +kubebuilder:object:root=true
type OvsDpdkResourcePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OvsDpdkResourcePolicy `json:"items"`
}
