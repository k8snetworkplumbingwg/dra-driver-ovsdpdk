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

package claimstore

import (
	k8stypes "k8s.io/apimachinery/pkg/types"

	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

// PreparedClaimStore is the interface consumed by the driver layer for
// persisting prepared device state between Prepare and Unprepare calls.
type PreparedClaimStore interface {
	Get(claimUID k8stypes.UID) ([]*dratypes.PreparedDevice, error)
	Set(claimUID k8stypes.UID, sc []*dratypes.PreparedDevice) error
	Delete(claimUID k8stypes.UID) error
	Close() error
}
