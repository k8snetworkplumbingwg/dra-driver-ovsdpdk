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

package podmanager_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/podmanager"
	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
)

var _ = Describe("BoltCheckpoint", func() {
	var (
		cp     podmanager.Checkpoint
		dbPath string
	)

	BeforeEach(func() {
		dbPath = filepath.Join(GinkgoT().TempDir(), "test.db")
		var err error
		cp, err = podmanager.NewBoltCheckpoint(dbPath)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if cp != nil {
			Expect(cp.Close()).To(Succeed())
		}
	})

	Describe("Load", func() {
		It("should return an empty map for a fresh database", func() {
			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeEmpty())
		})

		It("should return previously stored entries", func() {
			uid := k8stypes.UID("claim-1")
			devices := makePDs(uid, "test-claim")
			Expect(cp.Store(uid, devices)).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[uid]).To(HaveLen(1))
			Expect(result[uid][0].ClaimNamespacedName.Name).To(Equal("test-claim"))
		})
	})

	Describe("Store", func() {
		It("should persist a claim entry", func() {
			uid := k8stypes.UID("claim-store")
			devices := makePDs(uid, "stored-claim")
			Expect(cp.Store(uid, devices)).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveKey(uid))
		})

		It("should overwrite an existing entry", func() {
			uid := k8stypes.UID("claim-overwrite")
			Expect(cp.Store(uid, makePDs(uid, "first"))).To(Succeed())
			Expect(cp.Store(uid, makePDs(uid, "second"))).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result[uid][0].ClaimNamespacedName.Name).To(Equal("second"))
		})

		It("should store multiple independent claims", func() {
			uid1 := k8stypes.UID("claim-a")
			uid2 := k8stypes.UID("claim-b")
			Expect(cp.Store(uid1, makePDs(uid1, "a"))).To(Succeed())
			Expect(cp.Store(uid2, makePDs(uid2, "b"))).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(2))
		})

		It("should persist all PreparedDevice fields", func() {
			uid := k8stypes.UID("claim-full")
			devices := []*dratypes.PreparedDevice{
				{
					ClaimNamespacedName: kubeletplugin.NamespacedObject{
						NamespacedName: k8stypes.NamespacedName{
							Name:      "full-claim",
							Namespace: "test-ns",
						},
						UID: uid,
					},
					BridgeName:  "br-dpdk0",
					OVSPortName: "vhost-port",
					Mount: dratypes.MountInfo{
						HostDir:      "/var/run/ovsdpdk/host",
						ContainerDir: "/var/run/ovsdpdk/container",
					},
					Socket: dratypes.SocketInfo{
						HostPath:      "/var/run/ovsdpdk/host/vhost.sock",
						ContainerPath: "/var/run/ovsdpdk/container/vhost.sock",
					},
				},
			}
			Expect(cp.Store(uid, devices)).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			restored := result[uid][0]
			Expect(restored.BridgeName).To(Equal("br-dpdk0"))
			Expect(restored.OVSPortName).To(Equal("vhost-port"))
			Expect(restored.Mount.HostDir).To(Equal("/var/run/ovsdpdk/host"))
			Expect(restored.Mount.ContainerDir).To(Equal("/var/run/ovsdpdk/container"))
			Expect(restored.Socket.HostPath).To(Equal("/var/run/ovsdpdk/host/vhost.sock"))
			Expect(restored.Socket.ContainerPath).To(Equal("/var/run/ovsdpdk/container/vhost.sock"))
		})
	})

	Describe("Delete", func() {
		It("should remove a stored entry", func() {
			uid := k8stypes.UID("claim-delete")
			Expect(cp.Store(uid, makePDs(uid, "to-delete"))).To(Succeed())
			Expect(cp.Delete(uid)).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(HaveKey(uid))
		})

		It("should not error when deleting a nonexistent entry", func() {
			Expect(cp.Delete("nonexistent")).To(Succeed())
		})

		It("should not affect other entries", func() {
			uid1 := k8stypes.UID("keep")
			uid2 := k8stypes.UID("remove")
			Expect(cp.Store(uid1, makePDs(uid1, "keep"))).To(Succeed())
			Expect(cp.Store(uid2, makePDs(uid2, "remove"))).To(Succeed())
			Expect(cp.Delete(uid2)).To(Succeed())

			result, err := cp.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result).To(HaveKey(uid1))
		})
	})

	Describe("persistence across close/reopen", func() {
		It("should survive a simulated restart", func() {
			uid := k8stypes.UID("claim-persist")
			devices := makePDs(uid, "persistent-claim")
			Expect(cp.Store(uid, devices)).To(Succeed())

			// Close and reopen to simulate restart.
			Expect(cp.Close()).To(Succeed())
			cp = nil

			cp2, err := podmanager.NewBoltCheckpoint(dbPath)
			Expect(err).NotTo(HaveOccurred())
			defer func() {
				Expect(cp2.Close()).To(Succeed())
			}()

			result, err := cp2.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[uid][0].ClaimNamespacedName.Name).To(Equal("persistent-claim"))
		})

		It("should reflect deletes after restart", func() {
			uid := k8stypes.UID("claim-del-persist")
			Expect(cp.Store(uid, makePDs(uid, "del-persist"))).To(Succeed())
			Expect(cp.Delete(uid)).To(Succeed())

			Expect(cp.Close()).To(Succeed())
			cp = nil

			cp2, err := podmanager.NewBoltCheckpoint(dbPath)
			Expect(err).NotTo(HaveOccurred())
			defer func() {
				Expect(cp2.Close()).To(Succeed())
			}()

			result, err := cp2.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeEmpty())
		})
	})
})
