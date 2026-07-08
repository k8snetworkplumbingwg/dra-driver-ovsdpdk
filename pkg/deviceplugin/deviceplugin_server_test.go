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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func TestDP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DP Suite")
}

var _ = Describe("socketPath", func() {
	DescribeTable("sanitizes resource names into valid socket filenames",
		func(resourceName, expectedSuffix string) {
			path := socketPath(resourceName)
			Expect(path).To(HaveSuffix(expectedSuffix))
			Expect(path).To(HavePrefix(pluginapi.DevicePluginPath))
		},
		Entry("slashes become dashes", "example.com/my-resource", "example-com-my-resource.sock"),
		Entry("dots become dashes", "foo.bar/baz", "foo-bar-baz.sock"),
		Entry("simple name", "a/b", "a-b.sock"),
	)
})

var _ = Describe("Server", func() {
	Describe("devices", func() {
		It("returns the correct number of devices", func() {
			srv := newServer("example.com/res", 0, 5)
			Expect(srv.devices()).To(HaveLen(5))
		})

		It("marks all devices as Healthy", func() {
			srv := newServer("example.com/res", 0, 3)
			for _, d := range srv.devices() {
				Expect(d.Health).To(Equal(pluginapi.Healthy))
			}
		})

		It("sets the correct NUMA node on all devices", func() {
			srv := newServer("example.com/res", 2, 4)
			for _, d := range srv.devices() {
				Expect(d.Topology).NotTo(BeNil())
				Expect(d.Topology.Nodes).To(HaveLen(1))
				Expect(d.Topology.Nodes[0].ID).To(Equal(int64(2)))
			}
		})

		It("generates unique device IDs", func() {
			srv := newServer("example.com/res", 0, 10)
			ids := make(map[string]struct{})
			for _, d := range srv.devices() {
				ids[d.ID] = struct{}{}
			}
			Expect(ids).To(HaveLen(10))
		})
	})

	Describe("Allocate", func() {
		It("returns one empty ContainerAllocateResponse per container request", func() {
			srv := newServer("example.com/res", 0, 10)
			req := &pluginapi.AllocateRequest{
				ContainerRequests: []*pluginapi.ContainerAllocateRequest{
					{DevicesIds: []string{"device-0"}},
					{DevicesIds: []string{"device-1"}},
					{DevicesIds: []string{"device-2"}},
				},
			}
			resp, err := srv.Allocate(context.Background(), req)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.ContainerResponses).To(HaveLen(3))
			for _, cr := range resp.ContainerResponses {
				Expect(cr.Envs).To(BeEmpty())
				Expect(cr.Mounts).To(BeEmpty())
				Expect(cr.Devices).To(BeEmpty())
				Expect(cr.Annotations).To(BeEmpty())
			}
		})

		It("returns an empty response for zero container requests", func() {
			srv := newServer("example.com/res", 0, 10)
			resp, err := srv.Allocate(context.Background(), &pluginapi.AllocateRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.ContainerResponses).To(BeEmpty())
		})
	})

	Describe("GetDevicePluginOptions", func() {
		It("returns empty options", func() {
			srv := newServer("example.com/res", 0, 1)
			opts, err := srv.GetDevicePluginOptions(context.Background(), &pluginapi.Empty{})
			Expect(err).NotTo(HaveOccurred())
			Expect(opts.PreStartRequired).To(BeFalse())
			Expect(opts.GetPreferredAllocationAvailable).To(BeFalse())
		})
	})

	Describe("PreStartContainer", func() {
		It("returns an empty response", func() {
			srv := newServer("example.com/res", 0, 1)
			resp, err := srv.PreStartContainer(context.Background(), &pluginapi.PreStartContainerRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(resp).NotTo(BeNil())
		})
	})
})
