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

package podmanager

import (
	k8stypes "k8s.io/apimachinery/pkg/types"

	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

// Checkpoint is the persistence interface for the PodManager.
// Implementations provide durable storage; the PodManager remains the
// in-memory source of truth with write-through to the Checkpoint.
type Checkpoint interface {
	// Load loads persisted state or returns an empty map if no
	// checkpoint exists yet.
	Load() (map[k8stypes.UID][]*dratypes.PreparedDevice, error)

	// Store persists the entry for a single claim.
	Store(claimUID k8stypes.UID, devices []*dratypes.PreparedDevice) error

	// Delete removes a claim's entry from the persistent store.
	Delete(claimUID k8stypes.UID) error

	// Close releases resources held by the checkpointer.
	Close() error
}
