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

// Package driver implements the DRA kubelet plugin for OVS-DPDK vhost-user ports.
package driver

import (
	"context"
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	coreclientset "k8s.io/client-go/kubernetes"
	metadatav1alpha1 "k8s.io/dynamic-resource-allocation/api/metadata/v1alpha1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/podmanager"
)

// Driver is the DRA kubelet plugin for OVS-DPDK vhost-user ports.
type Driver struct {
	log         klog.Logger
	nodeName    string
	deviceState devicestate.DeviceStateIface
	podManager  *podmanager.PodManager
	helper      *kubeletplugin.Helper
	client      coreclientset.Interface
}

// Config encapsulates the Driver configuration.
type Config struct {
	NodeName             string
	EnableDeviceMetadata bool
	PluginDataDir        string
	CdiDir               string
}

// New creates a new Driver and registers it with kubelet.
func New(ctx context.Context, devState devicestate.DeviceStateIface, kubeClient coreclientset.Interface, config *Config) (*Driver, error) {
	logger := klog.FromContext(ctx).WithName("driver")

	d := &Driver{
		log:         logger,
		nodeName:    config.NodeName,
		deviceState: devState,
		podManager:  podmanager.New(),
		client:      kubeClient,
	}

	opts := []kubeletplugin.Option{
		kubeletplugin.DriverName(consts.DriverName),
		kubeletplugin.NodeName(config.NodeName),
		kubeletplugin.KubeClient(kubeClient),
		kubeletplugin.PluginDataDirectoryPath(config.PluginDataDir),
	}
	if config.EnableDeviceMetadata {
		opts = append(opts,
			kubeletplugin.EnableDeviceMetadata(true),
			kubeletplugin.MetadataVersions(metadatav1alpha1.SchemeGroupVersion),
			kubeletplugin.CDIDirectory(config.CdiDir),
		)
	}

	helper, err := kubeletplugin.Start(ctx, d, opts...)
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}

	d.helper = helper
	d.deviceState.SetRepublishCallback(d.PublishResources)
	d.log.Info("DRA driver started")
	return d, nil
}

// PublishResources publishes the current set of allocatable devices as a
// ResourceSlice to the Kubernetes API server.
func (d *Driver) PublishResources(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("PublishResources")

	allocatable := d.deviceState.GetAllocatableDevices()
	devices := make([]resourceapi.Device, 0, len(allocatable))
	for _, device := range allocatable {
		devices = append(devices, device.Device)
	}

	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			d.nodeName: {
				Slices: []resourceslice.Slice{
					{Devices: devices},
				},
			},
		},
	}

	logger.Info("Publishing resources", "devices", len(devices))
	return d.helper.PublishResources(ctx, resources)
}

func (d *Driver) HandleError(ctx context.Context, err error, msg string) {
	klog.FromContext(ctx).WithName("HandleError").Error(err, msg)
}

// Stop shuts down the DRA driver and deregisters from kubelet.
func (d *Driver) Stop() {
	d.helper.Stop()
}
