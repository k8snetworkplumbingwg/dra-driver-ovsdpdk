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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
)

const (
	// KindOvsPortConfig is the Kind value for OvsPortConfig.
	KindOvsPortConfig = "OvsPortConfig"

	// APIVersion is the apiVersion value for types in this package.
	APIVersion = consts.GroupName + "/v1alpha1"
)

// OvsPolicing configures ingress policing on an OVS port.
type OvsPolicing struct {
	// MaxRate is the maximum ingress rate in kbps (ingress_policing_rate).
	// Required. 0 means unlimited.
	MaxRate *uint32 `json:"max_rate"`

	// Burst is the maximum ingress burst size in kb (ingress_policing_burst).
	// Optional. 0 or unset means OVS default.
	// +optional
	Burst *uint32 `json:"burst,omitempty"`
}

// OvsPortConfig is the opaque per-allocation configuration embedded in a
// ResourceClaim. It carries user-specified values for OVS port properties.
type OvsPortConfig struct {
	metav1.TypeMeta `json:",inline"`

	// Vlan is the VLAN ID to configure on the OVS port (0-4095).
	// When unset, the port is untagged.
	// +optional
	Vlan *int `json:"vlan,omitempty"`

	// Policing configures ingress policing on the OVS port.
	// +optional
	Policing *OvsPolicing `json:"policing,omitempty"`
}

func DefaultOvsPortConfig() *OvsPortConfig {
	return &OvsPortConfig{}
}
