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

package driver

import (
	"context"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

func (d *Driver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[k8stypes.UID]kubeletplugin.PrepareResult, error) {
	logger := klog.FromContext(ctx).WithName("PrepareResourceClaims")
	result := make(map[k8stypes.UID]kubeletplugin.PrepareResult, len(claims))

	for _, claim := range claims {
		logger.V(1).Info("Preparing claim", "claim", claim.UID, "name", claim.Name, "namespace", claim.Namespace)
		logger.V(3).Info("Claim", "claim", claim)

		if preparedDevices, found := d.podManager.Get(claim.UID); found {
			logger.V(1).Info("Claim already prepared, returning cached result", "claim", claim.UID)
			result[claim.UID] = preparedDevicesToResult(preparedDevices)
			continue
		}

		preparedDevices, err := d.deviceState.PrepareResourceClaim(ctx, claim)
		if err != nil {
			logger.Error(err, "Failed to prepare claim", "claim", claim.UID)
			result[claim.UID] = kubeletplugin.PrepareResult{Err: err}
			return result, err
		}

		if err := d.podManager.Set(claim.UID, preparedDevices); err != nil {
			logger.Error(err, "Failed to persist prepared devices", "claim", claim.UID)
		}
		result[claim.UID] = preparedDevicesToResult(preparedDevices)
		d.updateClaimStatus(ctx, claim)
		logger.V(1).Info("Prepared claim", "claim", claim.UID, "name", claim.Name, "namespace", claim.Namespace, "result", preparedDevices)
	}
	logger.V(1).Info("Prepared claims", "result", result)

	return result, nil
}

func (d *Driver) updateClaimStatus(ctx context.Context, claim *resourceapi.ResourceClaim) {
	// Snapshot only our driver's device entries so that they survive a claim
	// pointer swap on conflict. Other drivers' entries will be preserved from
	// the refreshed claim.
	ownedDevices := filterDevicesByDriver(claim.Status.Devices, consts.DriverName)

	err := wait.ExponentialBackoffWithContext(ctx, consts.Backoff, func(ctx context.Context) (bool, error) {
		_, updateErr := d.client.ResourceV1().ResourceClaims(claim.Namespace).UpdateStatus(ctx, claim, metav1.UpdateOptions{})
		if updateErr == nil {
			return true, nil
		}

		if apierrors.IsConflict(updateErr) {
			d.log.V(2).Info("Conflict updating claim status, refreshing claim", "claimUID", claim.UID)
			freshClaim, fetchErr := d.client.ResourceV1().ResourceClaims(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
			if fetchErr != nil {
				d.log.V(2).Info("Failed to fetch fresh claim, will retry", "claimUID", claim.UID, "error", fetchErr)
				return false, nil
			}
			// Merge our owned entries into the refreshed claim, preserving
			// entries from other drivers.
			freshClaim.Status.Devices = mergeDeviceStatus(freshClaim.Status.Devices, ownedDevices, consts.DriverName)
			claim = freshClaim
			d.log.V(2).Info("Refreshed claim, retrying status update", "claimUID", claim.UID)
		} else {
			d.log.V(2).Info("Retrying claim status update", "claimUID", claim.UID, "error", updateErr)
		}
		return false, nil
	})

	if err != nil {
		d.log.Error(err, "Failed to update claim status after retries", "claimUID", claim.UID)
	} else {
		d.log.V(1).Info("Updated claim status", "claimUID", claim.UID)
	}
}

func (d *Driver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[k8stypes.UID]error, error) {
	logger := klog.FromContext(ctx).WithName("UnprepareResourceClaims")
	result := make(map[k8stypes.UID]error, len(claims))

	for _, claim := range claims {
		logger.V(1).Info("Unprepareing claim", "claim", claim.UID, "name", claim.Name, "namespace", claim.Namespace)

		pd := d.podManager.Delete(claim.UID)
		if pd == nil {
			logger.Info("Claim not found in pod manager, nothing to unprepare", "claim", claim.UID)
			result[claim.UID] = nil
			continue
		}

		if err := d.deviceState.UnprepareResourceClaim(ctx, pd); err != nil {
			logger.Error(err, "Failed to unprepare claim", "claim", claim.UID)
			result[claim.UID] = fmt.Errorf("unprepare claim %s: %w", claim.UID, err)
			// Reinsert perpared device in cache so that future retires can continue.
			if setErr := d.podManager.Set(claim.UID, pd); setErr != nil {
				logger.Error(setErr, "Failed to re-persist prepared devices after unprepare failure", "claim", claim.UID)
			}
			continue
		}

		result[claim.UID] = nil
		logger.V(1).Info("Unprepared claim", "claim", claim.UID, "name", claim.Name, "namespace", claim.Namespace)
	}

	return result, nil
}

func preparedDevicesToResult(preparedDevices []*dratypes.PreparedDevice) kubeletplugin.PrepareResult {
	devices := []kubeletplugin.Device{}
	for _, pd := range preparedDevices {
		devices = append(devices, pd.Device)
	}
	return kubeletplugin.PrepareResult{
		Devices: devices,
	}
}

// filterDevicesByDriver returns only the device status entries for the given driver.
func filterDevicesByDriver(devices []resourceapi.AllocatedDeviceStatus, driver string) []resourceapi.AllocatedDeviceStatus {
	var filtered []resourceapi.AllocatedDeviceStatus
	for _, d := range devices {
		if d.Driver == driver {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// mergeDeviceStatus merges owned device entries into the target slice.
// It first removes any existing entries for the given driver from target,
// then appends all owned entries. This preserves entries from other drivers
// while updating our own.
func mergeDeviceStatus(target, owned []resourceapi.AllocatedDeviceStatus, driver string) []resourceapi.AllocatedDeviceStatus {
	var merged []resourceapi.AllocatedDeviceStatus
	for _, d := range target {
		if d.Driver != driver {
			merged = append(merged, d)
		}
	}
	return append(merged, owned...)
}
