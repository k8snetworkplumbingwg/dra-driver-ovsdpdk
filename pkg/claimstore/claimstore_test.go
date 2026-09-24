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

package claimstore_test

import (
	"path/filepath"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/claimstore"
	dratypes "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/types"
	ovsportv1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsport/v1alpha1"
)

func TestClaimStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ClaimStore Suite")
}

var _ = Describe("PreparedClaimStore", func() {
	var (
		cs     claimstore.PreparedClaimStore
		dbPath string
	)

	BeforeEach(func() {
		dbPath = filepath.Join(GinkgoT().TempDir(), "test.db")
		var err error
		cs, err = claimstore.New(dbPath)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		Expect(cs.Close()).To(Succeed())
	})

	Describe("Get", func() {
		It("returns nil for an unknown claim UID", func() {
			got, err := cs.Get("unknown-uid")
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(BeNil())
		})

		It("returns the stored devices for a known claim UID", func() {
			uid := k8stypes.UID("uid-1")
			pd := makePDs(uid, "claim-1")
			Expect(cs.Set(uid, pd)).To(Succeed())

			got, err := cs.Get(uid)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal(pd))
		})
	})

	Describe("Set", func() {
		It("overwrites an existing entry", func() {
			uid := k8stypes.UID("uid-overwrite")
			Expect(cs.Set(uid, makePDs(uid, "first"))).To(Succeed())
			Expect(cs.Set(uid, makePDs(uid, "second"))).To(Succeed())

			got, err := cs.Get(uid)
			Expect(err).ToNot(HaveOccurred())
			Expect(got[0].ClaimNamespacedName.Name).To(Equal("second"))
		})

		It("stores independent entries for different UIDs", func() {
			uid1 := k8stypes.UID("uid-a")
			uid2 := k8stypes.UID("uid-b")
			pd1 := makePDs(uid1, "claim-a")
			pd2 := makePDs(uid2, "claim-b")

			Expect(cs.Set(uid1, pd1)).To(Succeed())
			Expect(cs.Set(uid2, pd2)).To(Succeed())

			got1, _ := cs.Get(uid1)
			got2, _ := cs.Get(uid2)
			Expect(got1).To(Equal(pd1))
			Expect(got2).To(Equal(pd2))
		})

		It("round-trips all PreparedDevice fields", func() {
			uid := k8stypes.UID("uid-full")
			vlan := 42
			portConfig := &ovsportv1alpha1.OvsPortConfig{
				Vlan: &vlan,
			}
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
					PortConfig: portConfig,
				},
			}
			Expect(cs.Set(uid, devices)).To(Succeed())

			got, err := cs.Get(uid)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(Equal(devices))
		})
	})

	Describe("Delete", func() {
		It("removes a stored entry", func() {
			uid := k8stypes.UID("uid-delete")
			Expect(cs.Set(uid, makePDs(uid, "to-delete"))).To(Succeed())
			Expect(cs.Delete(uid)).To(Succeed())

			got, err := cs.Get(uid)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(BeNil())
		})

		It("does not error when deleting a nonexistent entry", func() {
			Expect(cs.Delete("nonexistent")).To(Succeed())
		})

		It("does not affect other entries", func() {
			uid1 := k8stypes.UID("keep")
			uid2 := k8stypes.UID("remove")
			Expect(cs.Set(uid1, makePDs(uid1, "keep"))).To(Succeed())
			Expect(cs.Set(uid2, makePDs(uid2, "remove"))).To(Succeed())
			Expect(cs.Delete(uid2)).To(Succeed())

			got1, err := cs.Get(uid1)
			Expect(err).ToNot(HaveOccurred())
			Expect(got1).ToNot(BeNil())

			got2, err := cs.Get(uid2)
			Expect(err).ToNot(HaveOccurred())
			Expect(got2).To(BeNil())
		})
	})

	Describe("persistence across close and reopen", func() {
		It("survives a simulated restart", func() {
			uid := k8stypes.UID("uid-persist")
			Expect(cs.Set(uid, makePDs(uid, "persistent-claim"))).To(Succeed())
			Expect(cs.Close()).To(Succeed())

			var err error
			cs, err = claimstore.New(dbPath)
			Expect(err).ToNot(HaveOccurred())

			got, err := cs.Get(uid)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].ClaimNamespacedName.Name).To(Equal("persistent-claim"))
		})

		It("reflects deletes after restart", func() {
			uid := k8stypes.UID("uid-del-persist")
			Expect(cs.Set(uid, makePDs(uid, "del-persist"))).To(Succeed())
			Expect(cs.Delete(uid)).To(Succeed())
			Expect(cs.Close()).To(Succeed())

			var err error
			cs, err = claimstore.New(dbPath)
			Expect(err).ToNot(HaveOccurred())

			got, err := cs.Get(uid)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(BeNil())
		})

		It("preserves multiple entries across restart", func() {
			uid1 := k8stypes.UID("uid-multi-1")
			uid2 := k8stypes.UID("uid-multi-2")
			Expect(cs.Set(uid1, makePDs(uid1, "claim-1"))).To(Succeed())
			Expect(cs.Set(uid2, makePDs(uid2, "claim-2"))).To(Succeed())
			Expect(cs.Close()).To(Succeed())

			var err error
			cs, err = claimstore.New(dbPath)
			Expect(err).ToNot(HaveOccurred())

			got1, err := cs.Get(uid1)
			Expect(err).ToNot(HaveOccurred())
			Expect(got1).To(HaveLen(1))

			got2, err := cs.Get(uid2)
			Expect(err).ToNot(HaveOccurred())
			Expect(got2).To(HaveLen(1))
		})
	})

	Describe("thread safety", func() {
		It("handles concurrent Set and Get without data races", func() {
			const goroutines = 50
			var wg sync.WaitGroup
			wg.Add(goroutines * 2)

			for i := range goroutines {
				uid := k8stypes.UID("uid-concurrent-" + string(rune('A'+i)))
				pd := makePDs(uid, "claim-concurrent")

				go func() {
					defer wg.Done()
					_ = cs.Set(uid, pd)
				}()
				go func() {
					defer wg.Done()
					_, _ = cs.Get(uid)
				}()
			}
			wg.Wait()
		})

		It("handles concurrent Set and Delete without data races", func() {
			const goroutines = 50
			var wg sync.WaitGroup
			wg.Add(goroutines * 2)

			for i := range goroutines {
				uid := k8stypes.UID("uid-del-" + string(rune('A'+i)))
				pd := makePDs(uid, "claim-del")
				Expect(cs.Set(uid, pd)).To(Succeed())

				go func() {
					defer wg.Done()
					_ = cs.Set(uid, pd)
				}()
				go func() {
					defer wg.Done()
					_ = cs.Delete(uid)
				}()
			}
			wg.Wait()
		})
	})
})

// makePDs builds a slice with a single minimal PreparedDevice for testing.
func makePDs(uid k8stypes.UID, name string) []*dratypes.PreparedDevice {
	return []*dratypes.PreparedDevice{
		{
			ClaimNamespacedName: kubeletplugin.NamespacedObject{
				NamespacedName: k8stypes.NamespacedName{
					Name:      name,
					Namespace: "default",
				},
				UID: uid,
			},
		},
	}
}
