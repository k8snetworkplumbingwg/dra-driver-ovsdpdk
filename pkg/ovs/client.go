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

package ovs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ovn-kubernetes/libovsdb/cache"
	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/pci"
)

const (
	// DefaultOVSRunDir is the default OVS run directory.
	DefaultOVSRunDir = "/var/run/openvswitch"
)

// BridgeEventType indicates whether a bridge was added or deleted.
type BridgeEventType int

const (
	BridgeAdded BridgeEventType = iota
	BridgeDeleted
)

// BridgeEvent represents a bridge add/delete notification from the OVSDB monitor.
type BridgeEvent struct {
	Name string
	Type BridgeEventType
}

// ifaceEvent is an internal notification for a dpdk interface add or delete.
type ifaceEvent struct {
	uuid    string
	name    string
	devargs string // options["dpdk-devargs"], empty on delete
	added   bool
}

// OvsPortParams the OVS port parameters.
type OvsPortParams struct {
	// ExternalIDs is written verbatim to external_ids on the OVS Port row.
	ExternalIDs map[string]string
	// Vlan, when non-nil, sets the 802.1Q access VLAN tag on the port.
	Vlan *int
	// IngressRate sets ingress_policing_rate (kbps) on the Interface; 0 means unlimited.
	IngressRate int
	// IngressBurst sets ingress_policing_burst (kb) on the Interface; 0 means OVS default.
	IngressBurst int
	// Mtu, when non-nil, sets mtu_request on the Interface.
	// Valid range: 68 (RFC 791 minimum) to 65535.
	Mtu *int
}

// Client defines the interface for interacting with OVSDB.
type Client interface {
	// Connected reports whether the client currently has an active OVSDB connection.
	Connected() bool

	// Close disconnects from OVSDB.
	Close()

	// CreatePort creates an OVS port.
	CreatePort(ctx context.Context, bridgeName, portName, socketPath string, params *OvsPortParams) error

	// DeletePort delets an OVS port.
	DeletePort(ctx context.Context, bridgeName, portName string) error

	// SetBridgeNotifier sets a notifier callback for Bridge events.
	SetBridgeNotifier(fn func(BridgeEvent))

	// SetInterfaceNotifier sets a notifier callback for Interface events.
	SetInterfaceNotifier(fn func(bridgeName string))

	// BridgeExists returns true if the bridge is present in OVS.
	BridgeExists(name string) (bool, error)

	// BridgeNUMANodes return the deduplicated list of NUMA nodes from bridge uplinks.
	BridgeNUMANodes(bridgeName string) []int
}

// ovsClient wraps the libovsdb client for interacting with OVSDB.
type ovsClient struct {
	client         client.Client
	log            klog.Logger
	bridgeNotifier func(BridgeEvent)
	ifaceNotifier  func(bridgeName string)

	numaMu  sync.RWMutex
	numaMap map[string]map[string]int // bridgeName → ifaceUUID → numaNode
	ifaceCh chan ifaceEvent
}

// New creates an ovsClient and blocks until the initial OVSDB connection
// succeeds.
func New(ctx context.Context, runDir string) (*ovsClient, error) {
	endpoint := "unix:" + filepath.Join(runDir, "db.sock")

	dbModel, err := model.NewClientDBModel("Open_vSwitch",
		map[string]model.Model{
			"Open_vSwitch": &OpenvSwitch{},
			"Bridge":       &Bridge{},
			"Port":         &Port{},
			"Interface":    &Interface{},
		})
	if err != nil {
		return nil, fmt.Errorf("build OVSDB client model: %w", err)
	}

	ovs, err := client.NewOVSDBClient(dbModel,
		client.WithEndpoint(endpoint),
		client.WithReconnect(30*time.Second, backoff.NewExponentialBackOff()),
	)
	if err != nil {
		return nil, fmt.Errorf("create OVSDB client: %w", err)
	}

	log := klog.Background().WithName("ovsClient")
	log.Info("connecting to OVSDB", "endpoint", endpoint)

	for {
		if err := ovs.Connect(ctx); err == nil {
			break
		} else {
			log.V(2).Info("OVSDB connection failed, retrying in 5s", "endpoint", endpoint, "err", err)
		}

		select {
		case <-ctx.Done():
			ovs.Disconnect()
			return nil, fmt.Errorf("context cancelled while waiting for OVSDB at %q: %w", endpoint, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}

	log.Info("OVSDB connection established", "endpoint", endpoint)

	c := &ovsClient{
		client:  ovs,
		log:     log,
		numaMap: make(map[string]map[string]int),
		ifaceCh: make(chan ifaceEvent, 16),
	}

	if err := c.startMonitor(ctx); err != nil {
		ovs.Disconnect()
		return nil, fmt.Errorf("start bridge monitor: %w", err)
	}

	go c.processInterfaceEvents(ctx)

	return c, nil
}

// startMonitor registers cache event handlers for Bridge and Interface events,
// then starts a conditional OVSDB monitor that tracks:
//   - Bridge rows with datapath_type == "netdev" (DPDK bridges)
//   - Interface rows with type == "dpdk" (physical DPDK uplinks)
func (c *ovsClient) startMonitor(ctx context.Context) error {
	c.client.Cache().AddEventHandler(&cache.EventHandlerFuncs{
		AddFunc: func(table string, m model.Model) {
			switch table {
			case "Bridge":
				br, ok := m.(*Bridge)
				if !ok {
					return
				}
				c.log.V(2).Info("Bridge added", "name", br.Name, "datapathType", br.DatapathType)
				if c.bridgeNotifier != nil {
					c.bridgeNotifier(BridgeEvent{Name: br.Name, Type: BridgeAdded})
				}
			case "Interface":
				iface, ok := m.(*Interface)
				if !ok {
					return
				}
				devargs := iface.Options["dpdk-devargs"]
				c.log.V(2).Info("DPDK interface added", "name", iface.Name, "uuid", iface.UUID, "devargs", devargs)
				select {
				case c.ifaceCh <- ifaceEvent{uuid: iface.UUID, name: iface.Name, devargs: devargs, added: true}:
				default:
					c.log.V(2).Info("ifaceCh full, dropping add event", "interface", iface.Name)
				}
			}
		},
		DeleteFunc: func(table string, m model.Model) {
			switch table {
			case "Bridge":
				br, ok := m.(*Bridge)
				if !ok {
					return
				}
				c.log.V(2).Info("Bridge deleted", "name", br.Name)
				if c.bridgeNotifier != nil {
					c.bridgeNotifier(BridgeEvent{Name: br.Name, Type: BridgeDeleted})
				}
				// Clean up any NUMA entries for this bridge.
				c.numaMu.Lock()
				delete(c.numaMap, br.Name)
				c.numaMu.Unlock()
			case "Interface":
				iface, ok := m.(*Interface)
				if !ok {
					return
				}
				c.log.V(2).Info("DPDK interface deleted", "name", iface.Name, "uuid", iface.UUID)
				select {
				case c.ifaceCh <- ifaceEvent{uuid: iface.UUID, name: iface.Name, added: false}:
				default:
					c.log.V(2).Info("ifaceCh full, dropping delete event", "interface", iface.Name)
				}
			}
		},
	})

	bridgeProto := &Bridge{}
	ifaceProto := &Interface{}
	monitor := c.client.NewMonitor(
		// Only monitor bridges with datapath_type == "netdev" (DPDK bridges).
		client.WithConditionalTable(bridgeProto, []model.Condition{{
			Field:    &bridgeProto.DatapathType,
			Function: ovsdb.ConditionEqual,
			Value:    "netdev",
		}}),
		// Only monitor interfaces with type == "dpdk" (physical DPDK uplinks).
		client.WithConditionalTable(ifaceProto, []model.Condition{{
			Field:    &ifaceProto.Type,
			Function: ovsdb.ConditionEqual,
			Value:    "dpdk",
		}}),
	)
	if _, err := c.client.Monitor(ctx, monitor); err != nil {
		return fmt.Errorf("monitor Bridge/Interface tables: %w", err)
	}
	return nil
}

// Connected reports whether the client currently has an active OVSDB connection.
func (c *ovsClient) Connected() bool {
	return c.client.Connected()
}

// Close disconnects from OVSDB.
func (c *ovsClient) Close() {
	c.log.Info("Closing OVSDB client")
	c.client.Disconnect()
}

// SetBridgeNotifier sets a callback that is invoked whenever a DPDK bridge
// (datapath_type == "netdev") is added or deleted in OVSDB.
//
// The callback runs on the libovsdb cache goroutine, so it must not block.
func (c *ovsClient) SetBridgeNotifier(fn func(BridgeEvent)) {
	c.bridgeNotifier = fn
}

// SetInterfaceNotifier sets a callback that is invoked whenever a DPDK
// interface is added or removed from a bridge.
func (c *ovsClient) SetInterfaceNotifier(fn func(bridgeName string)) {
	c.ifaceNotifier = fn
}

// processInterfaceEvents is a background goroutine that reads ifaceEvents and
// updates the numaMap accordingly.
func (c *ovsClient) processInterfaceEvents(ctx context.Context) {
	for {
		select {
		case ev := <-c.ifaceCh:
			if ev.added {
				c.handleInterfaceAdd(ctx, ev)
			} else {
				c.handleInterfaceDelete(ev)
			}
		case <-ctx.Done():
			return
		}
	}
}

// handleInterfaceAdd resolves the bridge and NUMA node for a newly added dpdk
// interface and stores the result in numaMap.
func (c *ovsClient) handleInterfaceAdd(ctx context.Context, ev ifaceEvent) {
	pciAddr, err := pci.ParseDevargs(ev.devargs)
	if err != nil {
		c.log.Error(err, "Failed NUMA resolution", "interface", ev.name, "devargs", ev.devargs)
		return
	}

	numaNode, err := pci.NodeForPCIAddr(pciAddr)
	if err != nil {
		c.log.Error(err, "Failed to read NUMA node for DPDK interface",
			"interface", ev.name, "pciAddr", pciAddr)
		return
	}

	port, err := c.findPortWithIface(ctx, ev.uuid)
	if err != nil {
		c.log.Error(err, "Failed to find Port with Interface", "interface", ev.name)
	}

	bridge, err := c.findBridgeWithPort(ctx, port)
	if err != nil {
		c.log.Error(err, "Failed to find Bridge with Port", "port", port.Name)
	}

	// Store the bridge->Numa.
	c.numaMu.Lock()
	if c.numaMap[bridge.Name] == nil {
		c.numaMap[bridge.Name] = make(map[string]int)
	}
	c.numaMap[bridge.Name][ev.uuid] = numaNode
	c.numaMu.Unlock()

	c.log.V(2).Info("Resolved DPDK interface NUMA node",
		"interface", ev.name, "bridge", bridge.Name, "numaNode", numaNode)

	if c.ifaceNotifier != nil {
		c.ifaceNotifier(bridge.Name)
	}
}

// handleInterfaceDelete removes a deleted dpdk interface from numaMap.
func (c *ovsClient) handleInterfaceDelete(ev ifaceEvent) {
	c.numaMu.Lock()
	defer c.numaMu.Unlock()

	for bridgeName, ifaces := range c.numaMap {
		if _, ok := ifaces[ev.uuid]; ok {
			delete(ifaces, ev.uuid)
			if len(ifaces) == 0 {
				delete(c.numaMap, bridgeName)
			}
			c.log.V(2).Info("Removed DPDK interface from NUMA map",
				"interface", ev.name, "bridge", bridgeName)
			if c.ifaceNotifier != nil {
				c.ifaceNotifier(bridgeName)
			}
			return
		}
	}
}

// BridgeNUMANodes returns the deduplicated set of NUMA nodes for the DPDK
// uplink interfaces attached to the named bridge, as resolved from sysfs.
//
// Returns nil if no DPDK interface information is available yet for
// the bridge (e.g. the monitor has not yet observed any dpdk interfaces on it).
func (c *ovsClient) BridgeNUMANodes(bridgeName string) []int {
	c.numaMu.RLock()
	defer c.numaMu.RUnlock()

	ifaces, ok := c.numaMap[bridgeName]
	if !ok || len(ifaces) == 0 {
		return nil
	}

	seen := make(map[int]struct{})
	var nodes []int
	for _, node := range ifaces {
		if _, dup := seen[node]; !dup {
			seen[node] = struct{}{}
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// BridgeExists reports whether a bridge with the given name currently exists
// in the local OVSDB cache.
func (c *ovsClient) BridgeExists(name string) (bool, error) {
	br := Bridge{Name: name}
	if err := c.client.Get(context.Background(), &br); err == nil {
		return true, nil
	} else if err == client.ErrNotFound {
		return false, nil
	} else {
		return false, err
	}
}

// CreatePort creates a dpdkvhostuserclient OVS port and its associated Interface.
func (c *ovsClient) CreatePort(ctx context.Context, bridgeName, portName, socketPath string, params *OvsPortParams) error {
	iface := &Interface{
		UUID:                 "newiface",
		Name:                 portName,
		Type:                 "dpdkvhostuserclient",
		Options:              map[string]string{"vhost-server-path": socketPath},
		IngressPolicingRate:  params.IngressRate,
		IngressPolicingBurst: params.IngressBurst,
		MTURequest:           params.Mtu,
	}
	ifaceOps, err := c.client.Create(iface)
	if err != nil {
		return fmt.Errorf("build interface create op: %w", err)
	}

	port := &Port{
		UUID:        "newport",
		Name:        portName,
		Tag:         params.Vlan,
		Interfaces:  []string{"newiface"},
		ExternalIDs: params.ExternalIDs,
	}
	portOps, err := c.client.Create(port)
	if err != nil {
		return fmt.Errorf("build port create op: %w", err)
	}

	bridge := &Bridge{Name: bridgeName}
	mutateOps, err := c.client.Where(bridge).Mutate(bridge, model.Mutation{
		Field:   &bridge.Ports,
		Mutator: ovsdb.MutateOperationInsert,
		Value:   []string{"newport"},
	})
	if err != nil {
		return fmt.Errorf("build bridge mutate op: %w", err)
	}

	ops := append(append(ifaceOps, portOps...), mutateOps...)
	results, err := c.client.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("create port transaction: %w", err)
	}
	if opErrs, err := ovsdb.CheckOperationResults(results, ops); err != nil {
		return fmt.Errorf("create port %q on bridge %q: %w", portName, bridgeName, joinOpErrors(opErrs))
	}

	c.log.Info("Created OVS port", "bridge", bridgeName, "port", portName, "socket", socketPath, "params", params)
	return nil
}

// DeletePort removes the named OVS port (and its Interface) from the named bridge.
// Returns ErrPortNotFound (wrapped) if the port does not exist in OVSDB.
func (c *ovsClient) DeletePort(ctx context.Context, bridgeName, portName string) error {
	portUUID, err := c.findPortUUID(ctx, portName)
	if err != nil {
		return err
	}
	if portUUID == "" {
		return ErrPortNotFound
	}

	// Remove port from Bridge.Ports; OVSDB GC handles the rest.
	bridge := &Bridge{Name: bridgeName}
	ops, err := c.client.Where(bridge).Mutate(bridge, model.Mutation{
		Field:   &bridge.Ports,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   []string{portUUID},
	})
	if err != nil {
		return fmt.Errorf("build bridge mutate op: %w", err)
	}

	results, err := c.client.Transact(ctx, ops...)
	if err != nil {
		return fmt.Errorf("delete port transaction: %w", err)
	}
	if opErrs, err := ovsdb.CheckOperationResults(results, ops); err != nil {
		return fmt.Errorf("delete port %q from bridge %q: %w", portName, bridgeName, joinOpErrors(opErrs))
	}

	c.log.Info("Deleted OVS port", "bridge", bridgeName, "port", portName)
	return nil
}

// findPortUUID returns the OVSDB UUID of the named port via a Select
// query to avoid monitoring all ports.
func (c *ovsClient) findPortUUID(ctx context.Context, portName string) (string, error) {
	port := &Port{Name: portName}
	ops, err := c.client.Where(port).Select(port, &port.UUID)
	if err != nil {
		return "", fmt.Errorf("build select for port %q: %w", portName, err)
	}
	results, err := c.client.Transact(ctx, ops...)
	if err != nil {
		return "", fmt.Errorf("select port %q: %w", portName, err)
	}
	var ports []*Port
	if err := c.client.GetSelectResults(ops, results, &ports); err != nil {
		return "", fmt.Errorf("parse select results for port %q: %w", portName, err)
	}
	if len(ports) == 0 {
		return "", nil
	}
	return ports[0].UUID, nil
}

// findBridgeWithPort looks for the Bridge that contains an port
func (c *ovsClient) findBridgeWithPort(ctx context.Context, port *Port) (*Bridge, error) {
	// Bridges are monitored, look in cache.
	var bridges []*Bridge
	b := &Bridge{}

	err := c.client.WhereAll(b,
		model.Condition{
			Field:    &b.Ports,
			Function: ovsdb.ConditionIncludes,
			Value:    []string{port.UUID},
		},
	).List(ctx, &bridges)
	if err != nil {
		return nil, err
	}

	if len(bridges) == 0 {
		return nil, nil
	}
	if len(bridges) > 1 {
		return nil, fmt.Errorf("more than one bridge with port %s", port.Name)
	}
	return bridges[0], nil
}

// findPortWithIface looks for the Port that contains an interface.
func (c *ovsClient) findPortWithIface(ctx context.Context, ifaceUUID string) (*Port, error) {
	// Ports are not monitored, send query to the database.
	var ports []*Port
	port := &Port{}

	ops, err := c.client.WhereAll(port, model.Condition{
		Field:    &port.Interfaces,
		Function: ovsdb.ConditionIncludes,
		Value:    []string{ifaceUUID},
	}).Select(port)
	if err != nil {
		return nil, fmt.Errorf("build select for port with interface %q: %w", ifaceUUID, err)
	}

	results, err := c.client.Transact(ctx, ops...)
	if err != nil {
		return nil, fmt.Errorf("select port with interface %q: %w", ifaceUUID, err)
	}
	if err := c.client.GetSelectResults(ops, results, &ports); err != nil {
		return nil, fmt.Errorf("parse select results for port with interface %q: %w", ifaceUUID, err)
	}

	if len(ports) == 0 {
		return nil, nil
	}
	if len(ports) > 1 {
		return nil, fmt.Errorf("more than one port with interface %q", ifaceUUID)
	}

	return ports[0], nil
}

// joinOpErrors converts a slice of ovsdb.OperationError to a single error
// containing all individual error messages joined together.
func joinOpErrors(opErrs []ovsdb.OperationError) error {
	errs := make([]error, len(opErrs))
	for i, e := range opErrs {
		errs[i] = e
	}
	return errors.Join(errs...)
}
