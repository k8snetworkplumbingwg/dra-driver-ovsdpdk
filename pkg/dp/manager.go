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

package dp

import (
	"context"
	"sync"

	"k8s.io/klog/v2"

	ovsdpdkdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsdpdkdra/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs"
)

// Manager manages the lifecycle of topology Device Plugin servers.
// Manager reacts to two events:
// - Calls to UpdateResources(), called when bridge information is changed in the API.
// - Updates from OVS informing DPDK interfaces have been created or deleted from existing
// bridges
type Manager struct {
	mutex     sync.Mutex
	topology  map[string]string  // bridgeName → topologyResource
	servers   map[string]*Server // bridgeName → running Server
	ovsClient ovs.Client
	ctx       context.Context
	log       klog.Logger
}

// NewManager creates a Manager.
func NewManager(ctx context.Context, ovsClient ovs.Client) *Manager {
	m := &Manager{
		topology:  make(map[string]string),
		servers:   make(map[string]*Server),
		ovsClient: ovsClient,
		ctx:       ctx,
		log:       klog.Background().WithName("dp.Manager"),
	}
	return m
}

// UpdateResources reconciles the set of running Device Plugin servers against
// the provided bridge list.
func (m *Manager) UpdateResources(ctx context.Context, bridges []ovsdpdkdrav1alpha1.BridgeSpec) {
	logger := klog.FromContext(ctx).WithName("UpdateResources")

	// Rebuild topology map and detect duplicates.
	newTopology := make(map[string]string)
	for _, bridge := range bridges {
		if bridge.TopologyResource == "" {
			continue
		}
		newTopology[bridge.Name] = bridge.TopologyResource
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.topology = newTopology

	// Stop servers for bridges no longer in topology.
	for bridgeName, srv := range m.servers {
		if _, ok := m.topology[bridgeName]; !ok {
			logger.Info("Stopping topology Device Plugin", "bridge", bridgeName)
			srv.stop()
			delete(m.servers, bridgeName)
		}
	}

	// Ensure correct server state for each bridge in topology.
	for bridgeName, resourceName := range m.topology {
		m.ensureServer(bridgeName, resourceName)
	}
}

// ensureServer ensures the Device Plugin server for the given bridge is in the
// correct state based on the current NUMA topology.
func (m *Manager) ensureServer(bridgeName, resourceName string) {
	logger := m.log.WithName("ensureServer").WithValues("bridge", bridgeName)

	numaNode := m.getValidNUMA(logger, bridgeName)
	if numaNode < 0 {
		if srv, exists := m.servers[bridgeName]; exists {
			logger.Info("Stopping topology Device Plugin (NUMA no longer valid)",
				"bridge", bridgeName, "resource", resourceName)
			srv.stop()
			delete(m.servers, bridgeName)
		}
		return

	}

	if srv, exists := m.servers[bridgeName]; exists {
		// Correct NUMA, nothing to do.
		if srv.numaNode == numaNode {
			return
		}
		// NUMA changed, stop old server.
		logger.Info("NUMA node changed, recreating topology Device Plugin",
			"bridge", bridgeName, "resource", resourceName, "numaNode", numaNode)
		srv.stop()
		delete(m.servers, bridgeName)
	}

	// Start a new server.
	logger.Info("Starting topology Device Plugin",
		"bridge", bridgeName, "resource", resourceName, "numaNode", numaNode)
	srv := newServer(resourceName, numaNode, consts.DefaultTopologyDeviceCount)
	if err := srv.start(m.ctx); err != nil {
		logger.Error(err, "Failed to start topology Device Plugin",
			"bridge", bridgeName, "resource", resourceName)
		return
	}
	m.servers[bridgeName] = srv
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
		srv.stop()
		delete(m.servers, bridgeName)
	}
}
