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
	"errors"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/devicestate/mocks"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/podmanager"
	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

func TestDriver(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Driver Suite")
}

// newTestDriver builds a *Driver suitable for unit tests without starting the
// kubelet plugin gRPC server. helper is intentionally left nil because
// PrepareResourceClaims and UnprepareResourceClaims do not use it.
func newTestDriver(ds *mocks.MockDeviceStateIface, client *fake.Clientset) *Driver {
	pm, _ := podmanager.New(nil)
	return &Driver{
		log:         klog.Background(),
		deviceState: ds,
		podManager:  pm,
		client:      client,
	}
}

// makeClaim builds a minimal ResourceClaim with the fields required by the
// driver's Prepare path.
func makeClaim(uid, name, namespace string) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			UID:       k8stypes.UID(uid),
			Name:      name,
			Namespace: namespace,
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Driver:  "ovsdpdk.k8snetworkplumbingwg.io",
							Pool:    "test-node",
							Device:  "br-dpdk0",
							Request: "vhost-port",
						},
					},
				},
			},
			ReservedFor: []resourceapi.ResourceClaimConsumerReference{
				{UID: "pod-uid-1"},
			},
		},
	}
}

// makePreparedDevices returns a minimal slice of PreparedDevice for a claim.
func makePreparedDevices(claimUID, claimName, claimNamespace string) []*dratypes.PreparedDevice {
	return []*dratypes.PreparedDevice{
		{
			Device: kubeletplugin.Device{
				Requests:     []string{"vhost-port"},
				PoolName:     "test-node",
				DeviceName:   "br-dpdk0",
				CDIDeviceIDs: []string{"ovsdpdk.k8snetworkplumbingwg.io/vhost-user=abc123"},
			},
			ClaimNamespacedName: kubeletplugin.NamespacedObject{
				NamespacedName: k8stypes.NamespacedName{
					Name:      claimName,
					Namespace: claimNamespace,
				},
				UID: k8stypes.UID(claimUID),
			},
			BridgeName: "br-dpdk0",
		},
	}
}

// hasUpdateStatusAction returns true if the fake client recorded an UpdateStatus
// action for resourceclaims in the given namespace.
func hasUpdateStatusAction(client *fake.Clientset, namespace string) bool {
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" &&
			action.GetResource().Resource == "resourceclaims" &&
			action.GetSubresource() == "status" &&
			action.GetNamespace() == namespace {
			return true
		}
	}
	return false
}

var _ = Describe("PrepareResourceClaims", func() {
	var (
		ctx    context.Context
		ds     *mocks.MockDeviceStateIface
		client *fake.Clientset
		drv    *Driver
	)

	BeforeEach(func() {
		ctx = context.Background()
		ds = mocks.NewMockDeviceStateIface(GinkgoT())
		client = fake.NewClientset()
		drv = newTestDriver(ds, client)
	})

	Context("when the claim is already cached in the pod manager", func() {
		It("returns the cached result without calling deviceState or UpdateStatus again", func() {
			claim := makeClaim("uid-1", "claim-1", "default")
			_, _ = client.ResourceV1().ResourceClaims("default").Create(ctx, claim, metav1.CreateOptions{})

			callCount := 0
			ds.EXPECT().PrepareResourceClaim(mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ *resourceapi.ResourceClaim) ([]*dratypes.PreparedDevice, error) {
					callCount++
					return makePreparedDevices("uid-1", "claim-1", "default"), nil
				}).Once()

			// First call — prepares and caches.
			_, err := drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
			Expect(err).NotTo(HaveOccurred())
			Expect(callCount).To(Equal(1))
			client.ClearActions()

			// Second call with the same claim — must hit the cache, no further mock calls.
			result, err := drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
			Expect(err).NotTo(HaveOccurred())
			Expect(callCount).To(Equal(1))
			Expect(result[claim.UID].Devices).To(HaveLen(1))
			Expect(hasUpdateStatusAction(client, "default")).To(BeFalse())
		})
	})

	Context("when prepare succeeds", func() {
		var (
			claim  *resourceapi.ResourceClaim
			result map[k8stypes.UID]kubeletplugin.PrepareResult
		)

		BeforeEach(func() {
			claim = makeClaim("uid-2", "claim-2", "default")
			_, _ = client.ResourceV1().ResourceClaims("default").Create(ctx, claim, metav1.CreateOptions{})
			ds.EXPECT().PrepareResourceClaim(mock.Anything, mock.Anything).
				Return(makePreparedDevices("uid-2", "claim-2", "default"), nil).Once()
			var err error
			result, err = drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the correct devices in the result", func() {
			Expect(result[claim.UID].Err).To(BeNil())
			Expect(result[claim.UID].Devices).To(HaveLen(1))
			Expect(result[claim.UID].Devices[0].DeviceName).To(Equal("br-dpdk0"))
		})

		It("stores the prepared devices in the pod manager", func() {
			cached, found := drv.podManager.Get(claim.UID)
			Expect(found).To(BeTrue())
			Expect(cached).To(HaveLen(1))
		})

		It("calls UpdateStatus on the k8s client", func() {
			Expect(hasUpdateStatusAction(client, "default")).To(BeTrue())
		})
	})

	Context("when UpdateStatus returns a conflict on the first attempt", func() {
		It("retries, re-fetches the claim, and eventually succeeds", func() {
			claim := makeClaim("uid-conflict", "claim-conflict", "default")
			_, _ = client.ResourceV1().ResourceClaims("default").Create(ctx, claim, metav1.CreateOptions{})

			ds.EXPECT().PrepareResourceClaim(mock.Anything, mock.Anything).
				Return(makePreparedDevices("uid-conflict", "claim-conflict", "default"), nil).Once()

			// Inject a conflict error on the first UpdateStatus call only.
			conflictErr := apierrors.NewConflict(
				schema.GroupResource{Group: "resource.k8s.io", Resource: "resourceclaims"},
				"claim-conflict",
				errors.New("resource version mismatch"),
			)
			firstCall := true
			client.PrependReactor("update", "resourceclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "status" {
					return false, nil, nil
				}
				if firstCall {
					firstCall = false
					return true, nil, conflictErr
				}
				return false, nil, nil // let the default reactor handle subsequent calls
			})

			result, err := drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
			Expect(err).NotTo(HaveOccurred())
			Expect(result[claim.UID].Err).To(BeNil())
			Expect(hasUpdateStatusAction(client, "default")).To(BeTrue())
		})
	})

	Context("when deviceState.PrepareResourceClaim returns an error", func() {
		var (
			claim       *resourceapi.ResourceClaim
			prepareErr  error
			result      map[k8stypes.UID]kubeletplugin.PrepareResult
			returnedErr error
		)

		BeforeEach(func() {
			claim = makeClaim("uid-3", "claim-3", "default")
			prepareErr = errors.New("OVS port creation failed")
			ds.EXPECT().PrepareResourceClaim(mock.Anything, mock.Anything).
				Return(nil, prepareErr).Once()
			result, returnedErr = drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
		})

		It("returns the error", func() {
			Expect(returnedErr).To(MatchError(prepareErr))
		})

		It("puts the error in the result map", func() {
			Expect(result[claim.UID].Err).To(MatchError(prepareErr))
		})

		It("does not store anything in the pod manager", func() {
			_, found := drv.podManager.Get(claim.UID)
			Expect(found).To(BeFalse())
		})

		It("does not call UpdateStatus", func() {
			Expect(hasUpdateStatusAction(client, "default")).To(BeFalse())
		})
	})

	Context("when multiple claims are prepared and the first fails", func() {
		It("returns an error and stops processing further claims", func() {
			claim1 := makeClaim("uid-4", "claim-4", "default")
			claim2 := makeClaim("uid-5", "claim-5", "default")
			prepareErr := errors.New("first claim failed")

			ds.EXPECT().PrepareResourceClaim(mock.Anything, mock.Anything).
				Return(nil, prepareErr).Once()

			result, err := drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim1, claim2})
			Expect(err).To(HaveOccurred())
			Expect(result[claim1.UID].Err).To(MatchError(prepareErr))
			// Second claim was not processed — mock expectation of Once() enforces this.
			_, found := result[claim2.UID]
			Expect(found).To(BeFalse())
		})
	})
})

var _ = Describe("UnprepareResourceClaims", func() {
	var (
		ctx    context.Context
		ds     *mocks.MockDeviceStateIface
		client *fake.Clientset
		drv    *Driver
	)

	BeforeEach(func() {
		ctx = context.Background()
		ds = mocks.NewMockDeviceStateIface(GinkgoT())
		client = fake.NewClientset()
		drv = newTestDriver(ds, client)
	})

	// prepareClaim runs a full PrepareResourceClaims so that the claim is
	// cached in the pod manager, mirroring the real prepare→unprepare lifecycle.
	prepareClaim := func(claim *resourceapi.ResourceClaim) {
		_, _ = client.ResourceV1().ResourceClaims(claim.Namespace).Create(ctx, claim, metav1.CreateOptions{})
		ds.EXPECT().PrepareResourceClaim(mock.Anything, mock.Anything).
			Return(makePreparedDevices(string(claim.UID), claim.Name, claim.Namespace), nil).Once()
		_, err := drv.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
		Expect(err).NotTo(HaveOccurred())
	}

	Context("when the claim was never prepared", func() {
		It("returns nil error without calling deviceState", func() {
			// No mock expectations set — mockery will fail the test if any method is called.
			result, err := drv.UnprepareResourceClaims(ctx, []kubeletplugin.NamespacedObject{
				{UID: "uid-missing"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result["uid-missing"]).To(BeNil())
		})
	})

	Context("when unprepare succeeds", func() {
		It("removes the claim from the pod manager", func() {
			claim := makeClaim("uid-6", "claim-6", "default")
			prepareClaim(claim)

			ds.EXPECT().UnprepareResourceClaim(mock.Anything, mock.Anything).
				Return(nil).Once()

			result, err := drv.UnprepareResourceClaims(ctx, []kubeletplugin.NamespacedObject{
				{UID: claim.UID, NamespacedName: k8stypes.NamespacedName{Name: claim.Name, Namespace: claim.Namespace}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result[claim.UID]).To(BeNil())

			_, found := drv.podManager.Get(claim.UID)
			Expect(found).To(BeFalse())
		})
	})

	Context("when deviceState.UnprepareResourceClaim returns an error", func() {
		var (
			claim        *resourceapi.ResourceClaim
			unprepareErr error
			result       map[k8stypes.UID]error
		)

		BeforeEach(func() {
			claim = makeClaim("uid-7", "claim-7", "default")
			prepareClaim(claim)
			unprepareErr = errors.New("socket dir removal failed")
			ds.EXPECT().UnprepareResourceClaim(mock.Anything, mock.Anything).
				Return(unprepareErr).Once()
			var err error
			result, err = drv.UnprepareResourceClaims(ctx, []kubeletplugin.NamespacedObject{
				{UID: claim.UID},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the error for that claim", func() {
			Expect(result[claim.UID]).To(MatchError(unprepareErr))
		})

		It("re-inserts the claim into the pod manager for retry", func() {
			_, found := drv.podManager.Get(claim.UID)
			Expect(found).To(BeTrue())
		})
	})

	Context("when multiple claims are unprepared", func() {
		It("handles each independently", func() {
			claim1 := makeClaim("uid-8", "claim-8", "default")
			claim2 := makeClaim("uid-9", "claim-9", "default")
			prepareClaim(claim1)
			prepareClaim(claim2)

			unprepareErr := errors.New("fail claim-8")
			ds.EXPECT().UnprepareResourceClaim(mock.Anything, mock.MatchedBy(func(pds []*dratypes.PreparedDevice) bool {
				return len(pds) > 0 && pds[0].ClaimNamespacedName.UID == "uid-8"
			})).Return(unprepareErr).Once()
			ds.EXPECT().UnprepareResourceClaim(mock.Anything, mock.MatchedBy(func(pds []*dratypes.PreparedDevice) bool {
				return len(pds) > 0 && pds[0].ClaimNamespacedName.UID == "uid-9"
			})).Return(nil).Once()

			result, err := drv.UnprepareResourceClaims(ctx, []kubeletplugin.NamespacedObject{
				{UID: claim1.UID},
				{UID: claim2.UID},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result[claim1.UID]).To(MatchError(unprepareErr))
			Expect(result[claim2.UID]).To(BeNil())

			// claim-8 re-inserted, claim-9 removed.
			_, found1 := drv.podManager.Get(claim1.UID)
			_, found2 := drv.podManager.Get(claim2.UID)
			Expect(found1).To(BeTrue())
			Expect(found2).To(BeFalse())
		})
	})
})

var _ = Describe("preparedDevicesToResult", func() {
	It("returns empty devices for nil input", func() {
		result := preparedDevicesToResult(nil)
		Expect(result.Err).To(BeNil())
		Expect(result.Devices).To(BeEmpty())
	})

	It("maps each PreparedDevice to its Device field", func() {
		pds := []*dratypes.PreparedDevice{
			{Device: kubeletplugin.Device{DeviceName: "br-dpdk0", PoolName: "node-1"}},
			{Device: kubeletplugin.Device{DeviceName: "br-dpdk1", PoolName: "node-1"}},
		}
		result := preparedDevicesToResult(pds)
		Expect(result.Devices).To(HaveLen(2))
		Expect(result.Devices[0].DeviceName).To(Equal("br-dpdk0"))
		Expect(result.Devices[1].DeviceName).To(Equal("br-dpdk1"))
	})
})
