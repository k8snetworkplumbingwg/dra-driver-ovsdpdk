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

// Package ovs provides a libovsdb-based client for interacting with the
// Open vSwitch database (OVSDB).
package ovs

// OpenvSwitch represents a row in the Open_vSwitch root table.
type OpenvSwitch struct {
	UUID    string   `ovsdb:"_uuid"`
	Bridges []string `ovsdb:"bridges"`
}

// Bridge represents a row in the Bridge table.
type Bridge struct {
	UUID         string   `ovsdb:"_uuid"`
	Name         string   `ovsdb:"name"`
	Ports        []string `ovsdb:"ports"`
	DatapathType string   `ovsdb:"datapath_type"`
}

// Port represents a row in the Port table.
type Port struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Tag         *int              `ovsdb:"tag"`
	Interfaces  []string          `ovsdb:"interfaces"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

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

// Interface represents a row in the Interface table.
type Interface struct {
	UUID    string            `ovsdb:"_uuid"`
	Name    string            `ovsdb:"name"`
	Type    string            `ovsdb:"type"`
	Options map[string]string `ovsdb:"options"`
}

// ifaceEvent is an internal notification for a dpdk interface add or delete.
type ifaceEvent struct {
	uuid    string
	name    string
	devargs string // options["dpdk-devargs"], empty on delete
	added   bool
}
