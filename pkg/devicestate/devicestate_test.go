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

package devicestate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	ovsdpdkdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsdpdkdra/v1alpha1"
	ovsportv1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsport/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/cdi"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/devicestate"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs"
	ovsmocks "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs/mocks"
	socketfsmocks "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/socketfs/mocks"
)

func TestDeviceState(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DeviceState Suite")
}

var _ = Describe("DeviceState", func() {
	var ds *devicestate.DeviceState

	BeforeEach(func() {
		ds = devicestate.New(nil, socketfsmocks.NewMockSocketFS(GinkgoT()), nil)
	})

	Describe("GetAllocatableDevices", func() {
		It("should return an empty non-nil map when no devices are set", func() {
			devices := ds.GetAllocatableDevices()
			Expect(devices).NotTo(BeNil())
			Expect(devices).To(BeEmpty())
		})

		It("should return a copy that does not affect internal state when modified", func() {
			devices := ds.GetAllocatableDevices()
			devices["injected"] = devicestate.AllocatableDevice{}
			Expect(ds.GetAllocatableDevices()).To(BeEmpty())
		})
	})

	Describe("SetRepublishCallback", func() {
		It("should not call the callback during UpdatePolicyDevices if not set", func(ctx SpecContext) {
			Expect(ds.UpdatePolicyDevices(ctx, nil)).To(Succeed())
		})

		It("should call the callback after a successful UpdatePolicyDevices", func(ctx SpecContext) {
			called := false
			ds.SetRepublishCallback(func(_ context.Context) error {
				called = true
				return nil
			})
			Expect(ds.UpdatePolicyDevices(ctx, nil)).To(Succeed())
			Expect(called).To(BeTrue())
		})

		It("should propagate callback errors back to the caller", func(ctx SpecContext) {
			callbackErr := errors.New("publish failed")
			ds.SetRepublishCallback(func(_ context.Context) error {
				return callbackErr
			})
			Expect(ds.UpdatePolicyDevices(ctx, nil)).To(MatchError(ContainSubstring("publish failed")))
		})

		It("should not call the callback when bridge validation fails", func(ctx SpecContext) {
			called := false
			ds.SetRepublishCallback(func(_ context.Context) error {
				called = true
				return nil
			})
			bridges := []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br0"},
				{Name: "br0"},
			}
			Expect(ds.UpdatePolicyDevices(ctx, bridges)).NotTo(Succeed())
			Expect(called).To(BeFalse())
		})
	})

	Describe("UpdatePolicyDevices", func() {
		It("should succeed with an empty bridge list", func(ctx SpecContext) {
			Expect(ds.UpdatePolicyDevices(ctx, nil)).To(Succeed())
		})

		It("should succeed with unique bridge names", func(ctx SpecContext) {
			bridges := []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br0"},
				{Name: "br1"},
				{Name: "br2"},
			}
			Expect(ds.UpdatePolicyDevices(ctx, bridges)).To(Succeed())
		})

		It("should return an error when two bridges share the same name", func(ctx SpecContext) {
			bridges := []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br0"},
				{Name: "br1"},
				{Name: "br0"},
			}
			Expect(ds.UpdatePolicyDevices(ctx, bridges)).To(
				MatchError(ContainSubstring("duplicate bridge name")),
			)
		})

		It("should return an error when all bridges share the same name", func(ctx SpecContext) {
			bridges := []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br-phy0"},
				{Name: "br-phy0"},
			}
			Expect(ds.UpdatePolicyDevices(ctx, bridges)).To(
				MatchError(ContainSubstring(`"br-phy0"`)),
			)
		})

		It("should produce one device per bridge with the correct name", func(ctx SpecContext) {
			bridges := []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br0"},
				{Name: "br1"},
			}
			Expect(ds.UpdatePolicyDevices(ctx, bridges)).To(Succeed())
			devices := ds.GetAllocatableDevices()
			Expect(devices).To(HaveLen(2))
			Expect(devices).To(HaveKey("br0"))
			Expect(devices).To(HaveKey("br1"))
			Expect(devices["br0"].Name).To(Equal("br0"))
			Expect(devices["br1"].Name).To(Equal("br1"))
		})

		It("should set consumable capacity to DefaultBridgeCapacity and allow multiple allocations", func(ctx SpecContext) {
			bridges := []ovsdpdkdrav1alpha1.BridgeSpec{{Name: "br0"}}
			Expect(ds.UpdatePolicyDevices(ctx, bridges)).To(Succeed())
			device := ds.GetAllocatableDevices()["br0"]
			Expect(device.AllowMultipleAllocations).To(Equal(ptr.To(true)))
			cap, ok := device.Capacity["ovsdpdk.k8snetworkplumbingwg.io/ports"]
			Expect(ok).To(BeTrue())
			Expect(cap.Value.Value()).To(Equal(int64(consts.DefaultBridgeCapacity)))
		})

		It("should replace allocatable devices on successive calls", func(ctx SpecContext) {
			Expect(ds.UpdatePolicyDevices(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br0"}, {Name: "br1"},
			})).To(Succeed())
			Expect(ds.GetAllocatableDevices()).To(HaveLen(2))

			Expect(ds.UpdatePolicyDevices(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br2"},
			})).To(Succeed())
			devices := ds.GetAllocatableDevices()
			Expect(devices).To(HaveLen(1))
			Expect(devices).To(HaveKey("br2"))
		})

		It("should leave allocatable devices unchanged when validation fails", func(ctx SpecContext) {
			Expect(ds.UpdatePolicyDevices(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br0"},
			})).To(Succeed())

			Expect(ds.UpdatePolicyDevices(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				{Name: "br1"}, {Name: "br1"},
			})).NotTo(Succeed())
			devices := ds.GetAllocatableDevices()
			Expect(devices).To(HaveLen(1))
			Expect(devices).To(HaveKey("br0"))
		})
	})

	Describe("GetVhostUserConfig", func() {
		It("should return the configured spec after UpdateConfig", func(ctx SpecContext) {
			spec := &ovsdpdkdrav1alpha1.VhostUserSpec{
				ContainerRootPath: "/custom/container",
			}
			Expect(ds.UpdateConfig(ctx, &ovsdpdkdrav1alpha1.OvsDpdkConfigSpec{VhostUser: spec})).To(Succeed())
			cfg := ds.GetVhostUserConfig()
			Expect(cfg.ContainerRootPath).To(Equal("/custom/container"))
		})
	})
})

var _ = Describe("DeviceState prepare/unprepare", func() {
	Describe("PrepareResourceClaim", func() {
		It("should return an error when the claim has no allocation", func(ctx SpecContext) {
			ds, _, _, _ := newDeviceStateWithMocks(ctx, nil)
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default", UID: "uid-1"},
				Status:     resourceapi.ResourceClaimStatus{},
			}
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(MatchError(ContainSubstring("no allocation")))
		})

		It("should return an error when the claim has no ReservedFor entry", func(ctx SpecContext) {
			ds, _, _, _ := newDeviceStateWithMocks(ctx, nil)
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default", UID: "uid-2"},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{},
				},
			}
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(MatchError(ContainSubstring("no ReservedFor")))
		})

		It("should return an error when the claim has multiple ReservedFor entries", func(ctx SpecContext) {
			ds, _, _, _ := newDeviceStateWithMocks(ctx, nil)
			claim := &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default", UID: "uid-3"},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{},
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{Resource: "pods", Name: "pod-a", UID: "pod-uid-a"},
						{Resource: "pods", Name: "pod-b", UID: "pod-uid-b"},
					},
				},
			}
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(MatchError(ContainSubstring("multiple pods")))
		})

		It("should return an error when the allocation has no results", func(ctx SpecContext) {
			ds, mockFS, _, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Maybe()

			claim := makeClaim("uid-4", "pod-uid-4", "claim-4", "vhost0", "br0")
			claim.Status.Allocation.Devices.Results = nil
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(MatchError(ContainSubstring("no allocation results for driver")))
		})

		It("should fall back to claim.Name when the pod-claim-name annotation is absent", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, &ovsdpdkdrav1alpha1.VhostUserSpec{
				ContainerRootPath: "/container",
			})
			podUID := k8stypes.UID("pod-uid-handwritten")

			expectedHostDir := filepath.Join(consts.HostRootPath, string(podUID)+"_"+"my-hand-written-claim"+"_"+"req-0")
			mockFS.EXPECT().CreateSocketDir(mock.Anything, expectedHostDir, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000005", podUID, "my-hand-written-claim", "vhost0", "br0")
			delete(claim.Annotations, resourceapi.PodResourceClaimAnnotation)
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].Mount.HostDir).To(Equal(expectedHostDir))
			Expect(pd[0].Mount.ContainerDir).To(Equal("/container/my-hand-written-claim/req-0"))
		})

		It("should use the pod-local claim name for host and container paths", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, &ovsdpdkdrav1alpha1.VhostUserSpec{
				ContainerRootPath: "/container",
			})
			podUID := k8stypes.UID("pod-uid-ok")
			podClaimName := "vhost1"

			expectedHostDir := filepath.Join(consts.HostRootPath, string(podUID)+"_"+podClaimName+"_"+"req-0")
			mockFS.EXPECT().CreateSocketDir(mock.Anything, expectedHostDir, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000000", podUID, "my-pod-vhost1-xz123", podClaimName, "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].Mount.HostDir).To(Equal(expectedHostDir))
			Expect(pd[0].Mount.ContainerDir).To(Equal("/container/" + podClaimName + "/req-0"))
		})

		It("should set Socket.HostPath to vhost.sock inside Mount.HostDir", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000001", "pod-uid-sp", "claim-sp", "vhost-sp", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].Socket.HostPath).To(Equal(filepath.Join(pd[0].Mount.HostDir, "vhost.sock")))
			Expect(pd[0].Socket.ContainerPath).To(Equal(filepath.Join(pd[0].Mount.ContainerDir, "vhost.sock")))
		})

		It("should set Mount.ContainerDir from ContainerRootPath and pod-local claim name", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, &ovsdpdkdrav1alpha1.VhostUserSpec{
				ContainerRootPath: "/container/root",
			})
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000002", "pod-uid-cm", "claim-cm-xz456", "vhost2", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].Mount.ContainerDir).To(Equal("/container/root/vhost2/req-0"))
		})

		It("should populate BridgeName from the allocation result device", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000003", "pod-uid-bn", "claim-bn", "vhost-bn", "br-dpdk0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].BridgeName).To(Equal("br-dpdk0"))
		})

		It("should populate Device with the correct CDI device ID", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			claimUID := k8stypes.UID("abcdef12-0000-0000-0000-000000000004")
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim(claimUID, "pod-uid-dev", "claim-dev", "vhost-dev", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].Device.CDIDeviceIDs).To(HaveLen(1))
			Expect(pd[0].Device.CDIDeviceIDs[0]).To(Equal(cdi.DeviceID(claimUID, "br0", "req-0")))
		})

		It("should write a CDI spec file on success", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			claimUID := k8stypes.UID("abcdef12-0000-0000-0000-000000000005")
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim(claimUID, "pod-uid-cdi", "claim-cdi", "vhost-cdi", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].Device.CDIDeviceIDs).To(HaveLen(1))
			Expect(pd[0].Device.CDIDeviceIDs[0]).To(ContainSubstring("abcdef12"))
		})

		It("should clean up the socket directory when CDI spec creation fails", func(ctx SpecContext) {
			// Use a read-only CDI root to force CreateClaimSpecFile to fail.
			ds, mockFS, mockOVS, cdiRoot := newDeviceStateWithMocks(ctx, nil)
			Expect(os.Chmod(cdiRoot, 0o555)).To(Succeed())

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			// Rollback calls DeletePort.
			mockOVS.EXPECT().DeletePort(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000006", "pod-uid-cleanup", "claim-cleanup-xz789", "vhost-cleanup", "br0")
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(HaveOccurred())
		})

		It("should prepare multiple resources for a single claim", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, &ovsdpdkdrav1alpha1.VhostUserSpec{
				ContainerRootPath: "/container",
			})
			claimUID := k8stypes.UID("abcdef12-0000-0000-0000-multi0000001")
			podUID := k8stypes.UID("pod-uid-multi")
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Times(2)
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Times(2)

			claim := makeClaim(claimUID, podUID, "claim-multi", "vhost-multi", "br0")
			// Add a second allocation result to the same claim.
			claim.Status.Allocation.Devices.Results = append(
				claim.Status.Allocation.Devices.Results,
				resourceapi.DeviceRequestAllocationResult{
					Request: "req-1",
					Driver:  consts.DriverName,
					Pool:    "pool-1",
					Device:  "br1",
				},
			)

			pds, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pds).To(HaveLen(2))

			// Each device should have its own request, bridge, paths, and CDI ID.
			Expect(pds[0].BridgeName).To(Equal("br0"))
			Expect(pds[1].BridgeName).To(Equal("br1"))

			Expect(pds[0].Device.Requests).To(Equal([]string{"req-0"}))
			Expect(pds[1].Device.Requests).To(Equal([]string{"req-1"}))

			Expect(pds[0].Device.CDIDeviceIDs).To(HaveLen(1))
			Expect(pds[1].Device.CDIDeviceIDs).To(HaveLen(1))
			Expect(pds[0].Device.CDIDeviceIDs[0]).To(Equal(cdi.DeviceID(claimUID, "br0", "req-0")))
			Expect(pds[1].Device.CDIDeviceIDs[0]).To(Equal(cdi.DeviceID(claimUID, "br1", "req-1")))

			// Paths must be distinct per request.
			Expect(pds[0].Mount.HostDir).To(Equal(filepath.Join(consts.HostRootPath, string(podUID)+"_vhost-multi_req-0")))
			Expect(pds[1].Mount.HostDir).To(Equal(filepath.Join(consts.HostRootPath, string(podUID)+"_vhost-multi_req-1")))
			Expect(pds[0].Mount.ContainerDir).To(Equal("/container/vhost-multi/req-0"))
			Expect(pds[1].Mount.ContainerDir).To(Equal("/container/vhost-multi/req-1"))

			// Socket paths derive from mount dirs.
			Expect(pds[0].Socket.HostPath).To(Equal(filepath.Join(pds[0].Mount.HostDir, "vhost.sock")))
			Expect(pds[1].Socket.HostPath).To(Equal(filepath.Join(pds[1].Mount.HostDir, "vhost.sock")))

			// Both share the same claim identity.
			Expect(pds[0].ClaimNamespacedName.UID).To(Equal(claimUID))
			Expect(pds[1].ClaimNamespacedName.UID).To(Equal(claimUID))
		})

		It("should forward the current VhostUserConfig permissions to CreateSocketDir", func(ctx SpecContext) {
			spec1 := &ovsdpdkdrav1alpha1.VhostUserSpec{
				User: ovsdpdkdrav1alpha1.NewUserGroupIDFromID(1000),
			}
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, spec1)
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Times(2)

			// Test default container root path is applied.
			spec1.ContainerRootPath = consts.DefaultContainerRootPath
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, spec1).Return(nil).Once()

			_, err := ds.PrepareResourceClaim(ctx, makeClaim("uid-perm-1", "pod-uid-p1", "claim-p1", "vhost-p1", "br0"))
			Expect(err).NotTo(HaveOccurred())

			spec2 := &ovsdpdkdrav1alpha1.VhostUserSpec{
				ContainerRootPath: "/container",
				User:              ovsdpdkdrav1alpha1.NewUserGroupIDFromID(2000),
				Group:             ovsdpdkdrav1alpha1.NewUserGroupIDFromID(2000),
			}
			Expect(ds.UpdateConfig(ctx, &ovsdpdkdrav1alpha1.OvsDpdkConfigSpec{VhostUser: spec2})).To(Succeed())
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, spec2).Return(nil).Once()

			_, err = ds.PrepareResourceClaim(ctx, makeClaim("uid-perm-2", "pod-uid-p2", "claim-p2", "vhost-p2", "br0"))
			Expect(err).NotTo(HaveOccurred())
		})

		It("should call CreatePort with the correct bridge, port name and socket path", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			claimUID := k8stypes.UID("abcdef12-0000-0000-0000-000000000020")
			podUID := k8stypes.UID("pod-uid-ovs")

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			portName := expectedPortName(claimUID, "req-0")
			mockOVS.EXPECT().CreatePort(mock.Anything, "br-dpdk0", portName, expectedSocketPath(podUID, "vhost-ovs", "req-0"), mock.Anything).Return(nil).Once()

			claim := makeClaim(claimUID, podUID, "claim-ovs", "vhost-ovs", "br-dpdk0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].OVSPortName).To(Equal(portName))
		})

		It("should pass claim and pod metadata as external IDs to CreatePort", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			claimUID := k8stypes.UID("abcdef12-0000-0000-0000-000000000060")

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything,
				mock.MatchedBy(func(p *ovs.OvsPortParams) bool {
					return p.ExternalIDs["claim-uid"] == string(claimUID) &&
						p.ExternalIDs["claim-name"] == "claim-ext" &&
						p.ExternalIDs["namespace"] == "default" &&
						p.ExternalIDs["pod-name"] == "test-pod"
				}),
			).Return(nil).Once()

			claim := makeClaim(claimUID, "pod-uid-ext", "claim-ext", "vhost-ext", "br0")
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should pass VLAN tag to CreatePort params", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)

			cfg := ovsportv1alpha1.OvsPortConfig{}
			cfg.APIVersion = ovsportv1alpha1.APIVersion
			cfg.Kind = ovsportv1alpha1.KindOvsPortConfig
			cfg.Vlan = new(500)

			claim := makeClaimWithConfig(
				"abcdef12-0000-0000-0000-000000000000", "pod-uid-vlan",
				"claim", "vhost", "br0",
				[]resourceapi.DeviceAllocationConfiguration{makePortConfigEntry(cfg)},
			)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, "br0", mock.Anything, mock.Anything,
				mock.MatchedBy(func(p *ovs.OvsPortParams) bool {
					return p.Vlan != nil && *p.Vlan == 500
				}),
			).Return(nil).Once()

			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should pass QoS policing params to CreatePort", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything,
				mock.MatchedBy(func(p *ovs.OvsPortParams) bool {
					return p.IngressRate == 1000 && p.IngressBurst == 256
				}),
			).Return(nil).Once()

			portCfg := ovsportv1alpha1.OvsPortConfig{
				Policing: &ovsportv1alpha1.OvsPolicing{
					MaxRate: ptr.To(uint32(1000)),
					Burst:   ptr.To(uint32(256)),
				},
			}
			portCfg.APIVersion = ovsportv1alpha1.APIVersion
			portCfg.Kind = ovsportv1alpha1.KindOvsPortConfig
			raw, err := json.Marshal(portCfg)
			Expect(err).NotTo(HaveOccurred())

			claim := makeClaim("abcdef12-0000-0000-0000-000000000061", "pod-uid-qos", "claim-qos", "vhost-qos", "br0")
			claim.Status.Allocation.Devices.Config = []resourceapi.DeviceAllocationConfiguration{{
				Source: resourceapi.AllocationConfigSourceClaim,
				DeviceConfiguration: resourceapi.DeviceConfiguration{
					Opaque: &resourceapi.OpaqueDeviceConfiguration{
						Driver:     consts.DriverName,
						Parameters: runtime.RawExtension{Raw: raw},
					},
				},
			}}
			_, err = ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should roll back the socket directory when CreatePort fails", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("OVSDB transaction failed")).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000021", "pod-uid-ovs-fail", "claim-ovs-fail", "vhost-ovs-fail", "br0")
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("create OVS port")))
		})
	})

	Describe("UnprepareResourceClaim", func() {
		It("should remove the socket directory and CDI spec on success", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().DeletePort(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000010", "pod-uid-up", "claim-up", "vhost-up", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())

			Expect(ds.UnprepareResourceClaim(ctx, pd)).To(Succeed())
		})

		It("should return an error when the socket directory removal fails", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().DeletePort(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(fmt.Errorf("remove socket directory: permission denied")).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000011", "pod-uid-fail", "claim-fail", "vhost-fail", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())

			err = ds.UnprepareResourceClaim(ctx, pd)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("remove socket directory")))
		})

		It("should call DeletePort with the correct bridge and port name", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			claimUID := k8stypes.UID("abcdef12-0000-0000-0000-000000000040")
			portName := expectedPortName(claimUID, "req-0")

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, "br-dpdk0", portName, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().DeletePort(mock.Anything, "br-dpdk0", portName).Return(nil).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Once()

			claim := makeClaim(claimUID, "pod-uid-del", "claim-del", "vhost-del", "br-dpdk0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())

			Expect(ds.UnprepareResourceClaim(ctx, pd)).To(Succeed())
		})

		It("should tolerate ErrPortNotFound during unprepare", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().DeletePort(mock.Anything, mock.Anything, mock.Anything).Return(ovs.ErrPortNotFound).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000041", "pod-uid-gone", "claim-gone", "vhost-gone", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())

			Expect(ds.UnprepareResourceClaim(ctx, pd)).To(Succeed())
		})

		It("should return an error when DeletePort fails with a non-ErrPortNotFound error", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)

			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().DeletePort(mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("OVSDB connection lost")).Once()
			mockFS.EXPECT().RemoveSocketDir(mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000042", "pod-uid-delfail", "claim-delfail", "vhost-delfail", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())

			err = ds.UnprepareResourceClaim(ctx, pd)
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(ContainSubstring("OVSDB connection lost")))
		})
	})

	Describe("PrepareResourceClaim Device.Metadata", func() {
		It("should always populate Device.Metadata with vhost-user-path", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000030", "pod-uid-meta", "claim-meta", "vhost-meta", "br-dpdk0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())

			meta := pd[0].Device.Metadata
			Expect(meta).NotTo(BeNil())

			socketAttr, ok := meta.Attributes["vhost-user-path"]
			Expect(ok).To(BeTrue())
			Expect(socketAttr.StringValue).NotTo(BeNil())
			Expect(*socketAttr.StringValue).To(Equal(pd[0].Socket.ContainerPath))
			Expect(*socketAttr.StringValue).To(HavePrefix(consts.DefaultContainerRootPath))
		})
	})
})

var _ = Describe("DeviceState port config", func() {
	Describe("PrepareResourceClaim with OvsPortConfig", func() {
		It("should set PortConfig to the default when no opaque config is present", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

			claim := makeClaim("abcdef12-0000-0000-0000-000000000040", "pod-uid-pc0", "claim-pc0", "vhost-pc0", "br0")
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].PortConfig).NotTo(BeNil())
			Expect(*pd[0].PortConfig).To(Equal(*ovsportv1alpha1.DefaultOvsPortConfig()))
		})

		It("should parse a valid OvsPortConfig and store it in PortConfig", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

			cfg := ovsportv1alpha1.OvsPortConfig{}
			cfg.APIVersion = ovsportv1alpha1.APIVersion
			cfg.Kind = ovsportv1alpha1.KindOvsPortConfig

			claim := makeClaimWithConfig(
				"abcdef12-0000-0000-0000-000000000041", "pod-uid-pc1",
				"claim-pc1", "vhost-pc1", "br0",
				[]resourceapi.DeviceAllocationConfiguration{makePortConfigEntry(cfg)},
			)
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].PortConfig).NotTo(BeNil())
			Expect(pd[0].PortConfig.Kind).To(Equal(ovsportv1alpha1.KindOvsPortConfig))
			Expect(pd[0].PortConfig.APIVersion).To(Equal(ovsportv1alpha1.APIVersion))
		})

		It("should return an error when the opaque config has an invalid kind", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

			cfg := ovsportv1alpha1.OvsPortConfig{}
			cfg.APIVersion = ovsportv1alpha1.APIVersion
			cfg.Kind = "WrongKind"

			claim := makeClaimWithConfig(
				"abcdef12-0000-0000-0000-000000000042", "pod-uid-pc2",
				"claim-pc2", "vhost-pc2", "br0",
				[]resourceapi.DeviceAllocationConfiguration{makePortConfigEntry(cfg)},
			)
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(MatchError(ContainSubstring("unexpected kind")))
		})

		It("should store the vlan from OvsPortConfig in PortConfig", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

			cfg := ovsportv1alpha1.OvsPortConfig{}
			cfg.APIVersion = ovsportv1alpha1.APIVersion
			cfg.Kind = ovsportv1alpha1.KindOvsPortConfig
			cfg.Vlan = new(100)

			claim := makeClaimWithConfig(
				"abcdef12-0000-0000-0000-000000000043", "pod-uid-pc3",
				"claim-pc3", "vhost-pc3", "br0",
				[]resourceapi.DeviceAllocationConfiguration{makePortConfigEntry(cfg)},
			)
			pd, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).NotTo(HaveOccurred())
			Expect(pd[0].PortConfig).NotTo(BeNil())
			Expect(pd[0].PortConfig.Vlan).NotTo(BeNil())
			Expect(*pd[0].PortConfig.Vlan).To(Equal(100))
		})

		It("should return an error when vlan is out of range", func(ctx SpecContext) {
			ds, mockFS, mockOVS, _ := newDeviceStateWithMocks(ctx, nil)
			mockFS.EXPECT().CreateSocketDir(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			mockOVS.EXPECT().CreatePort(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

			cfg := ovsportv1alpha1.OvsPortConfig{}
			cfg.APIVersion = ovsportv1alpha1.APIVersion
			cfg.Kind = ovsportv1alpha1.KindOvsPortConfig
			cfg.Vlan = new(5000)

			claim := makeClaimWithConfig(
				"abcdef12-0000-0000-0000-000000000044", "pod-uid-pc4",
				"claim-pc4", "vhost-pc4", "br0",
				[]resourceapi.DeviceAllocationConfiguration{makePortConfigEntry(cfg)},
			)
			_, err := ds.PrepareResourceClaim(ctx, claim)
			Expect(err).To(MatchError(ContainSubstring("out of range")))
		})
	})
})

// newDeviceStateWithMocks creates a DeviceState with a real CDI temp directory,
// a mock SocketFS and a mock OVS client. The CDI temp dir is cleaned up via DeferCleanup.
func newDeviceStateWithMocks(ctx SpecContext, vhostUser *ovsdpdkdrav1alpha1.VhostUserSpec) (*devicestate.DeviceState, *socketfsmocks.MockSocketFS, *ovsmocks.MockClient, string) {
	GinkgoHelper()

	if vhostUser == nil {
		vhostUser = &ovsdpdkdrav1alpha1.VhostUserSpec{}
	}

	cdiRoot, err := os.MkdirTemp("", "cdi-root-*")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(os.RemoveAll, cdiRoot)

	mockFS := socketfsmocks.NewMockSocketFS(GinkgoT())
	mockOVS := ovsmocks.NewMockClient(GinkgoT())
	cdi, err := cdi.New(cdiRoot)
	Expect(err).NotTo(HaveOccurred())
	ds := devicestate.New(cdi, mockFS, mockOVS)
	Expect(ds.UpdateConfig(ctx, &ovsdpdkdrav1alpha1.OvsDpdkConfigSpec{VhostUser: vhostUser})).NotTo(HaveOccurred())
	return ds, mockFS, mockOVS, cdiRoot
}

// makeClaim builds a minimal ResourceClaim that satisfies PrepareResourceClaim.
// claimName is the auto-generated ResourceClaim name; podClaimName is the
// pod-local claim name stored in the standard annotation.
func makeClaim(claimUID, podUID k8stypes.UID, claimName, podClaimName, bridgeName string) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: "default",
			UID:       claimUID,
			Annotations: map[string]string{
				resourceapi.PodResourceClaimAnnotation: podClaimName,
			},
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req-0",
							Driver:  consts.DriverName,
							Pool:    "pool-0",
							Device:  bridgeName,
						},
					},
				},
			},
			ReservedFor: []resourceapi.ResourceClaimConsumerReference{
				{Resource: "pods", Name: "test-pod", UID: podUID},
			},
		},
	}
}

func makeClaimWithConfig(claimUID, podUID k8stypes.UID, claimName, podClaimName, bridgeName string,
	configs []resourceapi.DeviceAllocationConfiguration) *resourceapi.ResourceClaim {

	claim := makeClaim(claimUID, podUID, claimName, podClaimName, bridgeName)
	claim.Status.Allocation.Devices.Config = configs
	return claim
}

func makePortConfigEntry(cfg ovsportv1alpha1.OvsPortConfig) resourceapi.DeviceAllocationConfiguration {
	raw, err := json.Marshal(cfg)
	Expect(err).NotTo(HaveOccurred())
	return resourceapi.DeviceAllocationConfiguration{
		Source: resourceapi.AllocationConfigSourceClaim,
		DeviceConfiguration: resourceapi.DeviceConfiguration{
			Opaque: &resourceapi.OpaqueDeviceConfiguration{
				Driver:     consts.DriverName,
				Parameters: runtime.RawExtension{Raw: raw},
			},
		},
	}
}

// expectedPortName mirrors the production ovsPortName logic:
// first 8 hex chars of the UID (dashes stripped) + "-" + request.
func expectedPortName(claimUID k8stypes.UID, request string) string {
	uid := strings.ReplaceAll(string(claimUID), "-", "")
	return uid[:8] + "-" + request
}

// expectedSocketPath returns the host-side vhost socket path for a given
// pod UID, pod-local claim name and request.
func expectedSocketPath(podUID k8stypes.UID, podClaimName, request string) string {
	return filepath.Join(consts.HostRootPath, string(podUID)+"_"+podClaimName+"_"+request, consts.VhostSocketFilename)
}
