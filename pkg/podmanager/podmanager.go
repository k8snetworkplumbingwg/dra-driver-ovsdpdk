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

// Package podmanager caches prepared resource-claim state between prepare and
// unprepare calls in the driver layer.
package podmanager

import (
	"fmt"
	"sync"

	k8stypes "k8s.io/apimachinery/pkg/types"
	klog "k8s.io/klog/v2"

	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

// PodManager is a thread-safe cache of PreparedDevice records keyed by claim UID.
type PodManager struct {
	mu         sync.RWMutex
	byClaimUID map[k8stypes.UID][]*dratypes.PreparedDevice
	cp         Checkpoint
}

// New creates a new PodManager.
func New(cp Checkpoint) (*PodManager, error) {
	pm := &PodManager{
		byClaimUID: make(map[k8stypes.UID][]*dratypes.PreparedDevice),
		cp:         cp,
	}

	if cp != nil {
		restored, err := cp.Load()
		if err != nil {
			return nil, fmt.Errorf("restore checkpoint: %w", err)
		}
		pm.byClaimUID = restored
	}

	return pm, nil
}

// Get returns the PreparedDevice for the given claim UID.
func (pm *PodManager) Get(claimUID k8stypes.UID) ([]*dratypes.PreparedDevice, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	sc, ok := pm.byClaimUID[claimUID]
	return sc, ok
}

// Set stores the PreparedDevice for the given claim UID.
func (pm *PodManager) Set(claimUID k8stypes.UID, sc []*dratypes.PreparedDevice) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.byClaimUID[claimUID] = sc

	if pm.cp != nil {
		if err := pm.cp.Store(claimUID, sc); err != nil {
			return fmt.Errorf("checkpoint store: %w", err)
		}
	}
	return nil
}

// Delete removes and returns the PreparedDevice for the given claim UID.
// Returns nil if not found.
func (pm *PodManager) Delete(claimUID k8stypes.UID) []*dratypes.PreparedDevice {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	sc, ok := pm.byClaimUID[claimUID]
	if !ok {
		return nil
	}
	delete(pm.byClaimUID, claimUID)

	if pm.cp != nil {
		if err := pm.cp.Delete(claimUID); err != nil {
			klog.Errorf("Failed to delete checkpoint for claim %s: %v", claimUID, err)
		}
	}
	return sc
}

// Close releases resources held by the PodManager, if any.
func (pm *PodManager) Close() error {
	if pm.cp != nil {
		return pm.cp.Close()
	}
	return nil
}

// Len returns the number of claims in the store.
func (pm *PodManager) Len() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.byClaimUID)
}
