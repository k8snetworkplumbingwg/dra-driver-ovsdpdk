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

package deviceplugin

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	ovsdpdkdrav1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsdpdkdra/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
	dpmocks "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/deviceplugin/mocks"
	ovsmocks "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/ovs/mocks"
)

// bridgeSpec is a helper to build a BridgeSpec with a TopologyResource.
func bridgeSpec(name, topologyResource string) ovsdpdkdrav1alpha1.BridgeSpec {
	return ovsdpdkdrav1alpha1.BridgeSpec{Name: name, TopologyResource: topologyResource}
}

var _ = Describe("Manager", func() {
	var (
		ctx         context.Context
		ovs         *ovsmocks.MockClient
		mgr         *Manager
		createdSrvs []*dpmocks.MockTopologyDPServer
		origFactory func(string, int, int) TopologyDPServer
	)

	BeforeEach(func() {
		ctx = context.Background()
		ovs = ovsmocks.NewMockClient(GinkgoT())
		createdSrvs = nil

		ovs.EXPECT().SetInterfaceNotifier(mock.Anything).Once()

		// Save and restore the global factory around each test.
		origFactory = newServerFunc
		// Default factory panics if called unexpectedly — tests that expect
		// server creation must override newServerFunc themselves.
		newServerFunc = func(resourceName string, numaNode, deviceCount int) TopologyDPServer {
			Fail("unexpected call to newServerFunc — set a factory in the test")
			return nil
		}

		mgr = NewManager(ctx, ovs)
	})

	AfterEach(func() {
		newServerFunc = origFactory
	})

	Describe("UpdateResources", func() {
		Context("topology map management", func() {
			It("builds topology from bridges with TopologyResource", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
					{Name: "br1"}, // no TopologyResource
				})).To(Succeed())

				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.topology).To(HaveKeyWithValue("br0", consts.TopologyResourcePrefix+"topo-br0"))
				Expect(mgr.topology).NotTo(HaveKey("br1"))
			})

			It("clears topology when called with an empty bridge list", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					srv.EXPECT().Stop().Once()
					createdSrvs = append(createdSrvs, srv)
					return srv
				}
				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(mgr.UpdateResources(ctx, nil)).To(Succeed())

				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.topology).To(BeEmpty())
				Expect(mgr.servers).To(BeEmpty())
			})
		})

		Context("server lifecycle", func() {
			It("starts a server when the bridge has a valid single NUMA node", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{1}).Once()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(createdSrvs).To(HaveLen(1))
				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.servers).To(HaveKey("br0"))
			})

			It("does not start a server when NUMA is empty (no DPDK interfaces)", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return(nil).Once()

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(createdSrvs).To(BeEmpty())
			})

			It("does not start a server when NUMA affinity is unknown (-1)", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{-1}).Once()

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(createdSrvs).To(BeEmpty())
			})

			It("does not start a server when NUMA spans multiple nodes", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0, 1}).Once()

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(createdSrvs).To(BeEmpty())
			})

			It("stops a server when the bridge is removed from the list", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					srv.EXPECT().Stop().Once()
					createdSrvs = append(createdSrvs, srv)
					return srv
				}
				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(mgr.UpdateResources(ctx, nil)).To(Succeed())

				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.servers).NotTo(HaveKey("br0"))
			})

			It("does not restart a server when NUMA is unchanged", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Twice()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					// stop must NOT be called
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())
				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(createdSrvs).To(HaveLen(1))
			})

			It("recreates the server when the NUMA node changes", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{1}).Once()

				callCount := 0
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					callCount++
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					if callCount == 1 {
						srv.EXPECT().Stop().Once()
					}
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())
				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				Expect(createdSrvs).To(HaveLen(2))
				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.servers["br0"].GetNUMA()).To(Equal(1))
			})

			It("recreates the server when resource name changes but NUMA is unchanged", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Twice()

				callCount := 0
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					callCount++
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					if callCount == 1 {
						srv.EXPECT().Stop().Once()
					}
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())
				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0-new"),
				})).To(Succeed())

				Expect(createdSrvs).To(HaveLen(2))
				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.servers["br0"].GetResourceName()).To(Equal(consts.TopologyResourcePrefix + "topo-br0-new"))
			})

			It("stops the server when NUMA becomes invalid", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{-1}).Once()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(nil).Once()
					srv.EXPECT().Stop().Once()
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())
				Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})).To(Succeed())

				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.servers).NotTo(HaveKey("br0"))
			})

			It("handles start failure gracefully without storing the server", func() {
				ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
				newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
					srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
					srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
					srv.EXPECT().Start(mock.Anything).Return(fmt.Errorf("kubelet not available")).Once()
					createdSrvs = append(createdSrvs, srv)
					return srv
				}

				err := mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
					bridgeSpec("br0", "topo-br0"),
				})
				Expect(err).To(HaveOccurred())

				mgr.mutex.Lock()
				defer mgr.mutex.Unlock()
				Expect(mgr.servers).NotTo(HaveKey("br0"))
			})
		})
	})

	Describe("OnInterfaceChange", func() {
		It("starts a server when NUMA becomes available for a tracked bridge", func() {
			// Register bridge in topology with no NUMA yet.
			ovs.EXPECT().BridgeNUMANodes("br0").Return(nil).Once()
			Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				bridgeSpec("br0", "topo-br0"),
			})).To(Succeed())
			Expect(createdSrvs).To(BeEmpty())

			// Interface appears — NUMA now available.
			ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
			newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
				srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
				srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
				srv.EXPECT().Start(mock.Anything).Return(nil).Once()
				createdSrvs = append(createdSrvs, srv)
				return srv
			}
			mgr.OnInterfaceChange("br0")

			Expect(createdSrvs).To(HaveLen(1))
		})

		It("is a no-op for a bridge not in topology", func() {
			mgr.OnInterfaceChange("br-unknown")

			Expect(createdSrvs).To(BeEmpty())
		})

		It("stops the server when NUMA becomes invalid", func() {
			ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
			newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
				srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
				srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
				srv.EXPECT().Start(mock.Anything).Return(nil).Once()
				srv.EXPECT().Stop().Once()
				createdSrvs = append(createdSrvs, srv)
				return srv
			}
			Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				bridgeSpec("br0", "topo-br0"),
			})).To(Succeed())

			ovs.EXPECT().BridgeNUMANodes("br0").Return(nil).Once()
			mgr.OnInterfaceChange("br0")

			mgr.mutex.Lock()
			defer mgr.mutex.Unlock()
			Expect(mgr.servers).NotTo(HaveKey("br0"))
		})
	})

	Describe("StopAll", func() {
		It("stops all running servers", func() {
			ovs.EXPECT().BridgeNUMANodes("br0").Return([]int{0}).Once()
			ovs.EXPECT().BridgeNUMANodes("br1").Return([]int{1}).Once()
			newServerFunc = func(resourceName string, numaNode, _ int) TopologyDPServer {
				srv := dpmocks.NewMockTopologyDPServer(GinkgoT())
				srv.EXPECT().GetNUMA().Return(numaNode).Maybe()
					srv.EXPECT().GetResourceName().Return(resourceName).Maybe()
				srv.EXPECT().Start(mock.Anything).Return(nil).Once()
				srv.EXPECT().Stop().Once()
				createdSrvs = append(createdSrvs, srv)
				return srv
			}
			Expect(mgr.UpdateResources(ctx, []ovsdpdkdrav1alpha1.BridgeSpec{
				bridgeSpec("br0", "topo-br0"),
				bridgeSpec("br1", "topo-br1"),
			})).To(Succeed())
			Expect(createdSrvs).To(HaveLen(2))

			mgr.StopAll()

			mgr.mutex.Lock()
			defer mgr.mutex.Unlock()
			Expect(mgr.servers).To(BeEmpty())
		})

		It("is a no-op when no servers are running", func() {
			mgr.StopAll()
			Expect(createdSrvs).To(BeEmpty())
		})
	})
})
