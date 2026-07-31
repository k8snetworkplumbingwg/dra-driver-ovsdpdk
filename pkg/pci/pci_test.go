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

package pci_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/pci"
)

func TestPCI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PCI Suite")
}

// writeSysfsNUMA creates a fake sysfs numa_node file for the given PCI address
// under base and returns base so the caller can set SysfsBaseDir.
func writeSysfsNUMA(base, pciAddr, content string) {
	dir := filepath.Join(base, "bus", "pci", "devices", pciAddr)
	ExpectWithOffset(1, os.MkdirAll(dir, 0o755)).To(Succeed())
	ExpectWithOffset(1, os.WriteFile(filepath.Join(dir, "numa_node"), []byte(content), 0o644)).To(Succeed())
}

var _ = Describe("NodeForPCIAddr", func() {
	var origBase string

	BeforeEach(func() {
		origBase = pci.SysfsBaseDir
		pci.SysfsBaseDir = GinkgoT().TempDir()
	})

	AfterEach(func() {
		pci.SysfsBaseDir = origBase
	})

	It("returns the correct NUMA node", func() {
		writeSysfsNUMA(pci.SysfsBaseDir, "0000:01:00.0", "0\n")
		node, err := pci.NodeForPCIAddr("0000:01:00.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(node).To(Equal(0))
	})

	It("returns a non-zero NUMA node", func() {
		writeSysfsNUMA(pci.SysfsBaseDir, "0000:81:00.0", "1\n")
		node, err := pci.NodeForPCIAddr("0000:81:00.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(node).To(Equal(1))
	})

	It("returns -1 for devices with no NUMA affinity", func() {
		writeSysfsNUMA(pci.SysfsBaseDir, "0000:00:01.0", "-1\n")
		node, err := pci.NodeForPCIAddr("0000:00:01.0")
		Expect(err).NotTo(HaveOccurred())
		Expect(node).To(Equal(-1))
	})

	It("returns an error for a non-existent device", func() {
		_, err := pci.NodeForPCIAddr("0000:ff:ff.0")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("ParseDevargs", func() {
	DescribeTable("address-based devargs",
		func(devargs, expected string) {
			pci, err := pci.ParseDevargs(devargs)
			Expect(err).To(BeNil())
			Expect(pci).To(Equal(expected))
		},
		Entry("bare PCI address", "0000:01:00.0", "0000:01:00.0"),
		Entry("PCI address with extra key=val", "0000:01:00.0,representor=[0]", "0000:01:00.0"),
		Entry("PCI address with multiple extras", "0000:81:00.1,key=val,other=x", "0000:81:00.1"),
	)

	DescribeTable("unrecognised formats return error",
		func(devargs string) {
			_, err := pci.ParseDevargs(devargs)
			Expect(err).NotTo(BeNil())
		},
		Entry("virtual device class", "class=ethernet"),
		Entry("empty string", ""),
		Entry("partial address", "01:00.0"),
		Entry("random string", "notanaddress"),
	)
})
