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

package deviceplugin

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"k8s.io/klog/v2"

	ovsdpdkdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsdpdkdra/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs"
)

// ResourceUpdater is the interface used by the controller to update the
// Device Plugin manager with bridge configuration changes.
type ResourceUpdater interface {
	UpdateResources(ctx context.Context, bridges []ovsdpdkdrav1alpha1.BridgeSpec) error
}

// newServerFunc is the factory used by the Manager to create topology DP servers.
var newServerFunc = func(resourceName string, numaNode, deviceCount int) TopologyDPServer {
	return newServer(resourceName, numaNode, deviceCount)
}

// Manager manages the lifecycle of topology Device Plugin servers.
// Manager reacts to two events:
// - Calls to UpdateResources(), called when bridge information is changed in the API.
// - Updates from OVS informing DPDK interfaces have been created or deleted from existing
// bridges
type Manager struct {
	mutex     sync.Mutex
	topology  map[string]string           // bridgeName → topologyResource
	servers   map[string]TopologyDPServer // bridgeName → running Server
	ovsClient ovs.Client
	ctx       context.Context
	log       klog.Logger
}

// NewManager creates a Manager.
func NewManager(ctx context.Context, ovsClient ovs.Client) *Manager {
	m := &Manager{
		topology:  make(map[string]string),
		servers:   make(map[string]TopologyDPServer),
		ovsClient: ovsClient,
		ctx:       ctx,
		log:       klog.Background().WithName("dp.Manager"),
	}
	ovsClient.SetInterfaceNotifier(m.OnInterfaceChange)
	return m
}

// UpdateResources reconciles the set of running Device Plugin servers against
// the provided bridge list.
func (m *Manager) UpdateResources(ctx context.Context, bridges []ovsdpdkdrav1alpha1.BridgeSpec) error {
	logger := klog.FromContext(ctx).WithName("UpdateResources")

	// Rebuild topology map. The user-provided TopologyResource is a suffix;
	// we prepend the driver prefix to form the full extended resource name.
	newTopology := make(map[string]string)
	for _, bridge := range bridges {
		if bridge.TopologyResource == "" {
			continue
		}
		newTopology[bridge.Name] = consts.TopologyResourcePrefix + bridge.TopologyResource
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.topology = newTopology

	// Stop servers for bridges no longer in topology.
	for bridgeName, srv := range m.servers {
		if _, ok := m.topology[bridgeName]; !ok {
			logger.Info("Stopping topology Device Plugin", "bridge", bridgeName)
			srv.Stop()
			delete(m.servers, bridgeName)
		}
	}

	// Ensure correct server state for each bridge in topology.
	var errs []error
	for bridgeName, resourceName := range m.topology {
		if err := m.ensureServer(bridgeName, resourceName); err != nil {
			errs = append(errs, fmt.Errorf("bridge %s: %w", bridgeName, err))
		}
	}
	return errors.Join(errs...)
}

// OnInterfaceChange re-evaluates the Device Plugin server when a DPDK interface
// is added or removed from the bridge.
func (m *Manager) OnInterfaceChange(bridgeName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	resourceName, wanted := m.topology[bridgeName]
	if !wanted {
		// Bridge not configured for topology; ignore interface changes.
		return
	}
	if err := m.ensureServer(bridgeName, resourceName); err != nil {
		m.log.Error(err, "Failed to ensure server on interface change", "bridge", bridgeName)
	}
}

// ensureServer ensures the Device Plugin server for the given bridge is in the
// correct state based on the current NUMA topology.
func (m *Manager) ensureServer(bridgeName, resourceName string) error {
	logger := m.log.WithName("ensureServer").WithValues("bridge", bridgeName)

	numaNode := m.getValidNUMA(logger, bridgeName)

	// Check if existing server needs to be stopped
	if srv, exists := m.servers[bridgeName]; exists {
		// Keep server if NUMA and resource name unchanged
		if srv.GetNUMA() == numaNode && srv.GetResourceName() == resourceName {
			return nil
		}
		// Stop server: NUMA invalid, NUMA changed, or resource name changed
		switch {
		case numaNode < 0:
			logger.Info("Stopping topology Device Plugin (NUMA no longer valid)",
				"bridge", bridgeName, "resource", resourceName)
		case srv.GetNUMA() != numaNode:
			logger.Info("NUMA node changed, recreating topology Device Plugin",
				"bridge", bridgeName, "resource", resourceName, "numaNode", numaNode)
		default:
			logger.Info("Resource name changed, recreating topology Device Plugin",
				"bridge", bridgeName, "oldResource", srv.GetResourceName(), "newResource", resourceName)
		}
		srv.Stop()
		delete(m.servers, bridgeName)
	}

	// Don't start new server if NUMA is invalid
	if numaNode < 0 {
		return nil
	}

	// Start a new server
	logger.Info("Starting topology Device Plugin",
		"bridge", bridgeName, "resource", resourceName, "numaNode", numaNode)
	srv := newServerFunc(resourceName, numaNode, consts.DefaultTopologyDeviceCount)
	if err := srv.Start(m.ctx); err != nil {
		logger.Error(err, "Failed to start topology Device Plugin",
			"bridge", bridgeName, "resource", resourceName)
		return err
	}
	m.servers[bridgeName] = srv
	return nil
}

// getValidNUMA retrieves and validates the NUMA node from a bridge.
func (m *Manager) getValidNUMA(logger klog.Logger, bridgeName string) int {
	nodes := m.ovsClient.BridgeNUMANodes(bridgeName)

	switch {
	case len(nodes) == 0:
		logger.Info("Bridge has no DPDK interfaces yet, skipping topology Device Plugin")
	case len(nodes) > 1:
		logger.Error(nil, "Bridge uplinks span multiple distinct NUMA nodes, skipping topology Device Plugin", "numaNodes", nodes)
	case len(nodes) == 1 && nodes[0] < 0:
		logger.Info("Bridge NUMA affinity unknown, skipping topology Device Plugin")
	default:
		return nodes[0]
	}
	return -1
}

// StopAll stops all running Device Plugin servers.
func (m *Manager) StopAll() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for bridgeName, srv := range m.servers {
		m.log.Info("Stopping topology Device Plugin", "bridge", bridgeName)
		srv.Stop()
		delete(m.servers, bridgeName)
	}
}
