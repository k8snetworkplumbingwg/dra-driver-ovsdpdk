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

package devicestate

import (
	resourceapi "k8s.io/api/resource/v1"
)

// deviceStatusKey identifies one entry of ResourceClaim.status.devices.
// It matches the API server's listType=map composite key.
// A nil ShareID is mapped to the empty string so that two nil-ShareID entries
// compare equal via ordinary struct equality.
type deviceStatusKey struct {
	driver  string
	pool    string
	device  string
	shareID string
}

func keyOf(status resourceapi.AllocatedDeviceStatus) deviceStatusKey {
	key := deviceStatusKey{
		driver: status.Driver,
		pool:   status.Pool,
		device: status.Device,
	}
	if status.ShareID != nil {
		key.shareID = *status.ShareID
	}
	return key
}

// upsertDeviceStatus replaces the entry in list whose key (driver, pool,
// device and share ID) matches status, or appends status when none matches.
func upsertDeviceStatus(
	list []resourceapi.AllocatedDeviceStatus,
	status resourceapi.AllocatedDeviceStatus,
) []resourceapi.AllocatedDeviceStatus {
	key := keyOf(status)
	for i := range list {
		if keyOf(list[i]) == key {
			list[i] = status
			return list
		}
	}
	return append(list, status)
}
