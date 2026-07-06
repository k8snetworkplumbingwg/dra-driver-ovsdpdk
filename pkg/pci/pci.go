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

// Package pci provides helpers for PCI device topology resolution.
package pci

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SysfsBaseDir is the root of the sysfs filesystem.
var SysfsBaseDir = "/sys"

// NodeForPCIAddr returns the NUMA node of the PCI device at the given
// canonical address (e.g. "0000:01:00.0") by reading
// <SysfsBaseDir>/bus/pci/devices/<addr>/numa_node.
//
// The kernel reports -1 for devices on single-NUMA systems or with no
// NUMA affinity. Callers must treat -1 as "no NUMA affinity" and not as a
// valid node index.
func NodeForPCIAddr(pciAddr string) (int, error) {
	path := filepath.Join(SysfsBaseDir, "bus", "pci", "devices", pciAddr, "numa_node")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read numa_node for PCI device %q: %w", pciAddr, err)
	}
	node, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse numa_node for PCI device %q: %w", pciAddr, err)
	}
	return node, nil
}

// pciAddrRe matches a canonical PCI address: DDDD:BB:SS.F
var pciAddrRe = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)

// ParseDevargs extracts the PCI address from a DPDK devargs string.
func ParseDevargs(devargs string) (string, error) {
	fields := strings.Split(devargs, ",")

	if pciAddrRe.MatchString(fields[0]) {
		return fields[0], nil
	}

	return "", fmt.Errorf("unrecognized devargs format: %s", devargs)
}
