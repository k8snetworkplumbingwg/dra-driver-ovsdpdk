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

package devicestate

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ovsportv1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsport/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
)

// makeOpaqueClaimConfig builds a DeviceAllocationConfiguration entry with an
// OvsPortConfig payload sourced from a ResourceClaim.
func makeOpaqueClaimConfig(requests []string, cfg ovsportv1alpha1.OvsPortConfig) resourceapi.DeviceAllocationConfiguration {
	raw, err := json.Marshal(cfg)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return resourceapi.DeviceAllocationConfiguration{
		Source:   resourceapi.AllocationConfigSourceClaim,
		Requests: requests,
		DeviceConfiguration: resourceapi.DeviceConfiguration{
			Opaque: &resourceapi.OpaqueDeviceConfiguration{
				Driver:     consts.DriverName,
				Parameters: runtime.RawExtension{Raw: raw},
			},
		},
	}
}

// makeOpaqueClassConfig builds a DeviceAllocationConfiguration entry sourced
// from a DeviceClass (class-sourced).
func makeOpaqueClassConfig(requests []string, raw []byte) resourceapi.DeviceAllocationConfiguration {
	return resourceapi.DeviceAllocationConfiguration{
		Source:   resourceapi.AllocationConfigSourceClass,
		Requests: requests,
		DeviceConfiguration: resourceapi.DeviceConfiguration{
			Opaque: &resourceapi.OpaqueDeviceConfiguration{
				Driver:     consts.DriverName,
				Parameters: runtime.RawExtension{Raw: raw},
			},
		},
	}
}

var _ = Describe("parseClaimConfigs", func() {
	const request = "req-0"

	validConfig := func() ovsportv1alpha1.OvsPortConfig {
		cfg := ovsportv1alpha1.OvsPortConfig{}
		cfg.APIVersion = ovsportv1alpha1.APIVersion
		cfg.Kind = ovsportv1alpha1.KindOvsPortConfig
		return cfg
	}

	It("returns the default config when the config list is empty", func() {
		result, err := parseClaimConfigs(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.getConfig(request)).To(Equal(ovsportv1alpha1.DefaultOvsPortConfig()))
	})

	It("returns the default config when no entry matches the driver", func() {
		cfg := makeOpaqueClaimConfig(nil, validConfig())
		cfg.Opaque.Driver = "other.driver.io"
		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.getConfig(request)).To(Equal(ovsportv1alpha1.DefaultOvsPortConfig()))
	})

	It("returns the default config when the entry is scoped to a different request", func() {
		cfg := makeOpaqueClaimConfig([]string{"other-req"}, validConfig())
		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.getConfig(request)).To(Equal(ovsportv1alpha1.DefaultOvsPortConfig()))
	})

	It("matches an entry with empty Requests (applies to all)", func() {
		cfg := makeOpaqueClaimConfig(nil, validConfig())
		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).NotTo(HaveOccurred())
		got := result.getConfig(request)
		Expect(got.Kind).To(Equal(ovsportv1alpha1.KindOvsPortConfig))
	})

	It("matches an entry explicitly scoped to the request", func() {
		cfg := makeOpaqueClaimConfig([]string{request}, validConfig())
		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).NotTo(HaveOccurred())
		got := result.getConfig(request)
		Expect(got.Kind).To(Equal(ovsportv1alpha1.KindOvsPortConfig))
	})

	It("prefers a request-scoped entry over an applies-to-all entry", func() {
		allCfg := validConfig()
		allCfg.Vlan = new(100)
		reqCfg := validConfig()
		reqCfg.Vlan = new(200)

		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{
			makeOpaqueClaimConfig(nil, allCfg),
			makeOpaqueClaimConfig([]string{request}, reqCfg),
		})
		Expect(err).NotTo(HaveOccurred())
		got := result.getConfig(request)
		Expect(*got.Vlan).To(Equal(200))
		// A different request falls back to the applies-to-all entry.
		other := result.getConfig("other-req")
		Expect(*other.Vlan).To(Equal(100))
	})

	It("skips class-sourced entries without error", func() {
		raw, _ := json.Marshal(validConfig())
		cfg := makeOpaqueClassConfig(nil, raw)
		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.getConfig(request)).To(Equal(ovsportv1alpha1.DefaultOvsPortConfig()))
	})

	It("returns an error for an unexpected kind", func() {
		bad := validConfig()
		bad.Kind = "WrongKind"
		cfg := makeOpaqueClaimConfig(nil, bad)
		_, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).To(MatchError(ContainSubstring("unexpected kind")))
	})

	It("returns an error for an unexpected apiVersion", func() {
		bad := validConfig()
		bad.APIVersion = "wrong.group.io/v1"
		cfg := makeOpaqueClaimConfig(nil, bad)
		_, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).To(MatchError(ContainSubstring("unexpected apiVersion")))
	})

	It("returns an error for malformed JSON", func() {
		cfg := resourceapi.DeviceAllocationConfiguration{
			Source: resourceapi.AllocationConfigSourceClaim,
			DeviceConfiguration: resourceapi.DeviceConfiguration{
				Opaque: &resourceapi.OpaqueDeviceConfiguration{
					Driver:     consts.DriverName,
					Parameters: runtime.RawExtension{Raw: []byte(`{not valid json`)},
				},
			},
		}
		_, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).To(MatchError(ContainSubstring("unmarshal")))
	})

	It("returns the parsed config with correct apiVersion and kind", func() {
		cfg := makeOpaqueClaimConfig(nil, validConfig())
		result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{cfg})
		Expect(err).NotTo(HaveOccurred())
		got := result.getConfig(request)
		Expect(got.APIVersion).To(Equal(ovsportv1alpha1.APIVersion))
		Expect(got.Kind).To(Equal(ovsportv1alpha1.KindOvsPortConfig))
	})

	Describe("policing validation", func() {
		validPolicingConfig := func(maxRate uint32) ovsportv1alpha1.OvsPortConfig {
			cfg := validConfig()
			cfg.Policing = &ovsportv1alpha1.OvsPolicing{MaxRate: new(maxRate)}
			return cfg
		}

		It("accepts nil Policing (no policing configured)", func() {
			cfg := validConfig()
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.getConfig(request).Policing).To(BeNil())
		})

		It("accepts Policing with MaxRate only (Burst omitted)", func() {
			entry := makeOpaqueClaimConfig(nil, validPolicingConfig(100000))
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			got := result.getConfig(request)
			Expect(got.Policing).NotTo(BeNil())
			Expect(*got.Policing.MaxRate).To(Equal(uint32(100000)))
			Expect(got.Policing.Burst).To(BeNil())
		})

		It("accepts Policing with both MaxRate and Burst set", func() {
			cfg := validConfig()
			cfg.Policing = &ovsportv1alpha1.OvsPolicing{
				MaxRate: new(uint32(100000)),
				Burst:   new(uint32(10000)),
			}
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			got := result.getConfig(request)
			Expect(*got.Policing.MaxRate).To(Equal(uint32(100000)))
			Expect(*got.Policing.Burst).To(Equal(uint32(10000)))
		})

		It("accepts MaxRate = 0 (unlimited)", func() {
			entry := makeOpaqueClaimConfig(nil, validPolicingConfig(0))
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			Expect(*result.getConfig(request).Policing.MaxRate).To(Equal(uint32(0)))
		})

		It("accepts Burst = 0 (OVS default)", func() {
			cfg := validConfig()
			cfg.Policing = &ovsportv1alpha1.OvsPolicing{
				MaxRate: new(uint32(100000)),
				Burst:   new(uint32(0)),
			}
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			Expect(*result.getConfig(request).Policing.Burst).To(Equal(uint32(0)))
		})

		It("returns an error when Policing is set but MaxRate is nil", func() {
			cfg := validConfig()
			cfg.Policing = &ovsportv1alpha1.OvsPolicing{} // MaxRate intentionally nil
			entry := makeOpaqueClaimConfig(nil, cfg)
			_, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).To(MatchError(ContainSubstring("policing.max_rate is required")))
		})
	})

	Describe("vlan validation", func() {
		It("accepts a valid vlan", func() {
			cfg := validConfig()
			cfg.Vlan = new(100)
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			got := result.getConfig(request)
			Expect(got.Vlan).NotTo(BeNil())
			Expect(*got.Vlan).To(Equal(100))
		})

		It("accepts vlan = 0", func() {
			cfg := validConfig()
			cfg.Vlan = new(0)
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			got := result.getConfig(request)
			Expect(got.Vlan).NotTo(BeNil())
			Expect(*got.Vlan).To(Equal(0))
		})

		It("accepts vlan = 4095", func() {
			cfg := validConfig()
			cfg.Vlan = new(4095)
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			Expect(*result.getConfig(request).Vlan).To(Equal(4095))
		})

		It("returns an error for vlan > 4095", func() {
			cfg := validConfig()
			cfg.Vlan = new(4096)
			entry := makeOpaqueClaimConfig(nil, cfg)
			_, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).To(MatchError(ContainSubstring("out of range")))
		})

		It("returns an error for negative vlan", func() {
			cfg := validConfig()
			cfg.Vlan = new(-1)
			entry := makeOpaqueClaimConfig(nil, cfg)
			_, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).To(MatchError(ContainSubstring("out of range")))
		})

		It("leaves Vlan nil when not set", func() {
			cfg := validConfig()
			entry := makeOpaqueClaimConfig(nil, cfg)
			result, err := parseClaimConfigs([]resourceapi.DeviceAllocationConfiguration{entry})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.getConfig(request).Vlan).To(BeNil())
		})
	})
})
