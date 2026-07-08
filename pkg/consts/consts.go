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

package consts

const (
	// DriverName is the name of the DRA driver as registered with Kubernetes.
	DriverName = "ovsdpdk.k8snetworkplumbingwg.io"

	// GroupName is the API group name for this driver's CRDs.
	GroupName = "ovsdpdk.k8snetworkplumbingwg.io"

	// DefaultNamespace is the default namespace where the driver watches for OvsDpdkResourcePolicy resources.
	DefaultNamespace = "dra-driver-ovsdpdk"

	// DefaultBridgeCapacity is the default number of allocatable devices (ports) per bridge.
	DefaultBridgeCapacity = 32 * 1024

	// DefaultTopologyDeviceCount is the number of fake devices exposed by the
	// topology Device Plugin for each bridge.
	DefaultTopologyDeviceCount = 1024

	// TopologyResourcePrefix is the prefix for topology Device Plugin resource names.
	// The full resource name is TopologyResourcePrefix + user-provided suffix.
	TopologyResourcePrefix = DriverName + "/"

	// VhostSocketFilename is the name of the vhost-user socket file.
	VhostSocketFilename = "vhost.sock"

	// HostRootPath is the vhost-user host base path.
	HostRootPath = "/var/run/ovsdpdk"

	// DefaultContainerRootPath is the default vhost-user container base path.
	DefaultContainerRootPath = "/var/run/ovsdpdk"
)
