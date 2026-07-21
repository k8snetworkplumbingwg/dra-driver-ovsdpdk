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

// Package devicestate manages the lifecycle of vhost-user socket directories
// and their associated OVS ports on a given node.
package devicestate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	ovsdpdkdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsdpdkdra/v1alpha1"
	ovsportv1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsport/v1alpha1"
	dracdi "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/socketfs"
	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

// AllocatableDevice pairs a DRA device with its associated BridgeSpec.
type AllocatableDevice struct {
	resourceapi.Device
	BridgeSpec ovsdpdkdrav1alpha1.BridgeSpec
}

// AllocatableDevices maps bridge names to their allocatable device state.
type AllocatableDevices map[string]AllocatableDevice

// DeviceState manages the set of vhost-user devices advertised by this node
// and owns the prepare/unprepare lifecycle for resource claims.
type DeviceState struct {
	mutex             sync.RWMutex
	log               klog.Logger
	republishCallback func(ctx context.Context) error
	allocatable       AllocatableDevices
	vhostUserConfig   *ovsdpdkdrav1alpha1.VhostUserSpec
	cdi               *dracdi.Handler
	socketFS          socketfs.SocketFS
	ovsClient         ovs.Client
}

// deviceStatusData is the driver-specific debug payload written into
// ResourceClaim.Status.Devices[].Data after a successful prepare.
type deviceStatusData struct {
	Mount        dratypes.MountInfo             `json:"mount"`
	Socket       dratypes.SocketInfo            `json:"socket"`
	BridgeName   string                         `json:"bridgeName"`
	CDIDeviceIDs []string                       `json:"cdiDeviceID"`
	Config       *ovsportv1alpha1.OvsPortConfig `json:"config,omitempty"`
}

// New creates a new DeviceState with the given CDI handler, SocketFS and OVS client.
func New(cdi *dracdi.Handler, socketFS socketfs.SocketFS, ovsClient ovs.Client) *DeviceState {
	ds := &DeviceState{
		log:         klog.Background().WithName("DeviceState"),
		allocatable: AllocatableDevices{},
		cdi:         cdi,
		socketFS:    socketFS,
		ovsClient:   ovsClient,
	}
	ds.updateBridges(make([]ovsdpdkdrav1alpha1.BridgeSpec, 0))
	return ds
}

// UpdateConfig is called by the controller whenever the OvsDpdkConfig object changes.
// spec is nil when the config object does not exist.
func (d *DeviceState) UpdateConfig(ctx context.Context, spec *ovsdpdkdrav1alpha1.OvsDpdkConfigSpec) error {
	logger := klog.FromContext(ctx).WithName("UpdateConfig")
	if spec == nil || spec.VhostUser == nil {
		return fmt.Errorf("missing VhostUser configuration")
	}
	d.updateConfig(spec.VhostUser)
	logger.Info("Config updated", "config", spec.VhostUser)
	return nil
}

// SetRepublishCallback sets a callback that is invoked after UpdatePolicyDevices
// successfully updates the set of allocatable devices.
func (d *DeviceState) SetRepublishCallback(callback func(ctx context.Context) error) {
	d.republishCallback = callback
}

// GetAllocatableDevices returns a copy of the current set of allocatable devices.
func (d *DeviceState) GetAllocatableDevices() AllocatableDevices {
	d.mutex.RLock()
	defer d.mutex.RUnlock()
	return maps.Clone(d.allocatable)
}

// GetVhostUserConfig returns the effective vhost-user configuration. If no
// policy has set one, defaults are returned.
func (d *DeviceState) GetVhostUserConfig() *ovsdpdkdrav1alpha1.VhostUserSpec {
	d.mutex.RLock()
	defer d.mutex.RUnlock()
	return d.vhostUserConfig
}

func (d *DeviceState) updateConfig(vhostUser *ovsdpdkdrav1alpha1.VhostUserSpec) {
	vhostUserConfig := vhostUser.DeepCopy()

	// Apply default values
	if vhostUserConfig.ContainerRootPath == "" {
		vhostUserConfig.ContainerRootPath = consts.DefaultContainerRootPath
	}

	d.mutex.Lock()
	d.vhostUserConfig = vhostUserConfig
	d.mutex.Unlock()
}

func (d *DeviceState) updateBridges(bridges []ovsdpdkdrav1alpha1.BridgeSpec) {
	d.mutex.Lock()
	d.allocatable = computeAllocatableDevices(bridges)
	d.mutex.Unlock()
}

// UpdatePolicyDevices is called by the controller whenever the set of matching
// OvsDpdkResourcePolicy objects changes. bridges is the consolidated list of
// bridge specs that apply to this node.
func (d *DeviceState) UpdatePolicyDevices(ctx context.Context, bridges []ovsdpdkdrav1alpha1.BridgeSpec) error {
	logger := klog.FromContext(ctx).WithName("UpdatePolicyDevices")
	logger.Info("Updating policy devices", "bridges", len(bridges))

	seen := make(map[string]struct{}, len(bridges))
	for _, b := range bridges {
		if _, dup := seen[b.Name]; dup {
			return fmt.Errorf("duplicate bridge name %q across OvsDpdkResourcePolicy objects", b.Name)
		}
		seen[b.Name] = struct{}{}
	}

	d.updateBridges(bridges)

	logger.Info("Allocatable devices updated", "bridges", slices.Collect(maps.Keys(d.allocatable)))
	logger.V(2).Info("Allocatable devices updated", "devices", d.allocatable)

	if d.republishCallback != nil {
		if err := d.republishCallback(ctx); err != nil {
			logger.Error(err, "Republish callback failed")
			return fmt.Errorf("republish callback: %w", err)
		}
	}

	return nil
}

// PrepareResourceClaim prepares all devices in a resource claim. It creates a
// socket directory per device and writes the CDI spec.
func (d *DeviceState) PrepareResourceClaim(ctx context.Context, claim *resourceapi.ResourceClaim) ([]*dratypes.PreparedDevice, error) {
	logger := klog.FromContext(ctx).WithName("PrepareResourceClaim")
	preparedDevices := make([]*dratypes.PreparedDevice, 0)

	if claim.Status.Allocation == nil {
		return nil, fmt.Errorf("claim %s/%s has no allocation", claim.Namespace, claim.Name)
	}
	if len(claim.Status.ReservedFor) == 0 {
		return nil, fmt.Errorf("claim %s/%s has no ReservedFor entry", claim.Namespace, claim.Name)
	}
	if len(claim.Status.ReservedFor) > 1 {
		return nil, fmt.Errorf("multiple pods found for claim %s/%s not supported", claim.Namespace, claim.Name)
	}

	portConfigs, err := parseClaimConfigs(claim.Status.Allocation.Devices.Config)
	if err != nil {
		return nil, fmt.Errorf("parse claim configs: %w", err)
	}

	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != consts.DriverName {
			continue
		}

		preparedDevice, err := d.prepareDevice(ctx, claim, &result, portConfigs.getConfig(result.Request))
		if err != nil {
			logger.Error(err, "error preparing device", "result", result)
			return nil, d.rollback(ctx, fmt.Errorf("error preparing device: %v", err), preparedDevices)
		}
		updateClaimStatus(ctx, claim, result, preparedDevice)
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	if len(preparedDevices) == 0 {
		return nil, fmt.Errorf("no allocation results for driver %s", consts.DriverName)
	}

	if err := d.cdi.CreateClaimSpecFile(preparedDevices); err != nil {
		logger.Error(err, "error creating CDI spec file")
		return nil, d.rollback(ctx, fmt.Errorf("error creating CDI spec file: %v", err), preparedDevices)
	}

	return preparedDevices, nil
}

func (d *DeviceState) prepareDevice(ctx context.Context, claim *resourceapi.ResourceClaim, result *resourceapi.DeviceRequestAllocationResult, portConfig *ovsportv1alpha1.OvsPortConfig) (*dratypes.PreparedDevice, error) {
	logger := klog.FromContext(ctx).WithName("prepareDevice")

	podUID := k8stypes.UID(claim.Status.ReservedFor[0].UID)

	vhostConfig := d.GetVhostUserConfig()
	if vhostConfig == nil {
		return nil, fmt.Errorf("missing VhostUser configuration")
	}

	socketDir := getSocketDir(podUID, claim, result)
	if err := d.socketFS.CreateSocketDir(ctx, socketDir, d.GetVhostUserConfig()); err != nil {
		return nil, fmt.Errorf("create socket directory %q: %w", socketDir, err)
	}

	hostSocketPath := filepath.Join(socketDir, consts.VhostSocketFilename)
	portName := ovsPortName(claim.UID, result.Request)
	params := ovsPortParams(claim, portConfig)

	logger.Info("creating OVS port", "name", portName, "socket", hostSocketPath, "params", params)
	if err := d.ovsClient.CreatePort(ctx, result.Device, portName, hostSocketPath, params); err != nil {
		_ = d.socketFS.RemoveSocketDir(socketDir)
		return nil, fmt.Errorf("create OVS port %q on bridge %q: %w", portName, result.Device, err)
	}

	cdiDeviceID := dracdi.DeviceID(claim.UID, result.Device, result.Request)
	containerDir := getContainerDir(vhostConfig.ContainerRootPath, claim, result.Request)
	containerSocketPath := filepath.Join(containerDir, consts.VhostSocketFilename)

	pd := &dratypes.PreparedDevice{
		Device: kubeletplugin.Device{
			Requests:     []string{result.Request},
			PoolName:     result.Pool,
			DeviceName:   result.Device,
			CDIDeviceIDs: []string{cdiDeviceID},
			Metadata: &kubeletplugin.DeviceMetadata{
				Attributes: map[string]resourceapi.DeviceAttribute{
					"vhost-user-path": {StringValue: ptr.To(containerSocketPath)},
				},
			},
		},
		ClaimNamespacedName: kubeletplugin.NamespacedObject{
			NamespacedName: k8stypes.NamespacedName{
				Name:      claim.Name,
				Namespace: claim.Namespace,
			},
			UID: claim.UID,
		},
		BridgeName:  result.Device,
		OVSPortName: portName,
		Mount: dratypes.MountInfo{
			HostDir:      socketDir,
			ContainerDir: containerDir,
		},
		Socket: dratypes.SocketInfo{
			HostPath:      hostSocketPath,
			ContainerPath: containerSocketPath,
		},
		PortConfig: portConfig,
	}

	logger.Info("Prepared successful", "device", pd)
	return pd, nil
}

func (d *DeviceState) rollback(ctx context.Context, err error, devices []*dratypes.PreparedDevice) error {
	rollbackErrs := []error{err}

	for _, device := range devices {
		if rollbackErr := d.unprepareDevice(ctx, device); rollbackErr != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback unprepare failed: %w", rollbackErr))
		}
	}

	return errors.Join(rollbackErrs...)
}

// UnprepareResourceClaim removes the CDI spec and socket directory for each prepared device.
func (d *DeviceState) UnprepareResourceClaim(ctx context.Context, preparedDevices []*dratypes.PreparedDevice) error {
	var errs []error

	for _, pd := range preparedDevices {
		if err := d.unprepareDevice(ctx, pd); err != nil {
			errs = append(errs, fmt.Errorf("unprepare failed: %w", err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (d *DeviceState) unprepareDevice(ctx context.Context, pd *dratypes.PreparedDevice) error {
	logger := klog.FromContext(ctx).WithName("unprepareDevice")
	claimUID := pd.ClaimNamespacedName.UID
	var errs []error

	if err := d.cdi.DeleteClaimSpecFile(claimUID); err != nil {
		logger.Error(err, "Failed to delete CDI spec", "claimUID", claimUID)
	}

	if err := d.ovsClient.DeletePort(ctx, pd.BridgeName, pd.OVSPortName); err != nil {
		if errors.Is(err, ovs.ErrPortNotFound) {
			logger.Info("OVS port already gone, continuing cleanup", "port", pd.OVSPortName, "bridge", pd.BridgeName)
		} else {
			logger.Error(err, "Failed to delete OVS port", "port", pd.OVSPortName, "bridge", pd.BridgeName)
			errs = append(errs, err)
		}
	}

	if err := d.socketFS.RemoveSocketDir(pd.Mount.HostDir); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	logger.Info("Cleaned up claim resources", "claimUID", claimUID, "socketDir", pd.Mount.HostDir)
	return nil
}

// getContainerDir returns the container-side mount point for a given claim and request.
func getContainerDir(root string, claim *resourceapi.ResourceClaim, requestName string) string {
	return filepath.Join(root, getPodClaimName(claim), requestName)
}

// getSocketDir returns the socket directory for a given claim and request.
func getSocketDir(podUID k8stypes.UID, claim *resourceapi.ResourceClaim, result *resourceapi.DeviceRequestAllocationResult) string {
	return filepath.Join(consts.HostRootPath, string(podUID)+"_"+getPodClaimName(claim)+"_"+result.Request)
}

// updateClaimStatus writes driver debug data into ResourceClaim.Status.Devices
// after a successful prepare.
func updateClaimStatus(
	ctx context.Context,
	claim *resourceapi.ResourceClaim,
	allocResult resourceapi.DeviceRequestAllocationResult,
	pd *dratypes.PreparedDevice,
) {
	logger := klog.FromContext(ctx).WithName("updateClaimStatus")

	payload, err := json.Marshal(deviceStatusData{
		Mount:        pd.Mount,
		Socket:       pd.Socket,
		BridgeName:   pd.BridgeName,
		CDIDeviceIDs: pd.Device.CDIDeviceIDs,
		Config:       pd.PortConfig,
	})
	if err != nil {
		logger.Error(err, "Failed to marshal claim status data", "claimUID", claim.UID)
		return
	}

	claim.Status.Devices = append(claim.Status.Devices, resourceapi.AllocatedDeviceStatus{
		Driver:  allocResult.Driver,
		Pool:    allocResult.Pool,
		Device:  allocResult.Device,
		ShareID: (*string)(allocResult.ShareID),
		Data:    &runtime.RawExtension{Raw: payload},
	})
}

// computeAllocatableDevices converts a list of bridge specs into DRA device specifications.
func computeAllocatableDevices(bridges []ovsdpdkdrav1alpha1.BridgeSpec) AllocatableDevices {
	devices := make(AllocatableDevices, len(bridges))
	for _, bridge := range bridges {
		devices[bridge.Name] = bridgeToDevice(bridge)
	}
	return devices
}

func bridgeToDevice(bridge ovsdpdkdrav1alpha1.BridgeSpec) AllocatableDevice {
	one := resource.NewQuantity(1, resource.DecimalSI)
	return AllocatableDevice{
		Device: resourceapi.Device{
			Name:                     bridge.Name,
			AllowMultipleAllocations: ptr.To(true),
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				consts.DriverName + "/" + "bridgeName": {
					StringValue: ptr.To(bridge.Name),
				},
			},
			Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
				consts.DriverName + "/" + "ports": {
					Value: *resource.NewQuantity(consts.DefaultBridgeCapacity, resource.DecimalSI),
					RequestPolicy: &resourceapi.CapacityRequestPolicy{
						Default: one,
						ValidRange: &resourceapi.CapacityRequestPolicyRange{
							Min:  resource.NewQuantity(1, resource.DecimalSI),
							Step: one,
						},
					},
				},
			},
		},
		BridgeSpec: bridge,
	}
}

// ovsPortName derives a stable OVS port/interface name from the first 8 hex
// chars of the claim UID (dashes stripped) and the request name.
func ovsPortName(claimUID k8stypes.UID, request string) string {
	uid := strings.ReplaceAll(string(claimUID), "-", "")
	return uid[:8] + "-" + request
}

// ovsPortParams creates the port parameters for a request.
func ovsPortParams(claim *resourceapi.ResourceClaim, portConfig *ovsportv1alpha1.OvsPortConfig) *ovs.OvsPortParams {
	params := &ovs.OvsPortParams{
		ExternalIDs: map[string]string{
			"claim-uid":  string(claim.UID),
			"claim-name": claim.Name,
			"namespace":  claim.Namespace,
			"pod-name":   claim.Status.ReservedFor[0].Name,
		},
		Vlan: portConfig.Vlan,
	}
	if portConfig.Policing != nil {
		if portConfig.Policing.MaxRate != nil {
			params.IngressRate = int(*portConfig.Policing.MaxRate)
		}
		if portConfig.Policing.Burst != nil {
			params.IngressBurst = int(*portConfig.Policing.Burst)
		}
	}
	return params
}

// getPodClaimName returns the stable name of a claim in a Pod.
// For claims created from a ResourceClaimTemplate the kubelet sets the
// pod-local claim name in a standard annotation. For hand-written claims
// the annotation is absent and claim.Name is already stable.
func getPodClaimName(claim *resourceapi.ResourceClaim) string {
	podClaimName := claim.Annotations[resourceapi.PodResourceClaimAnnotation]
	if podClaimName == "" {
		podClaimName = claim.Name
	}
	return podClaimName
}
