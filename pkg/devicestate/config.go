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
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/klog/v2"

	ovsportv1alpha1 "github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/api/ovsport/v1alpha1"
	"github.com/k8snetworkplumbingwg/dra-driver-ovsdpdk/pkg/consts"
)

// claimPortConfigs holds parsed per-request and default port configurations.
type claimPortConfigs struct {
	byRequest  map[string]*ovsportv1alpha1.OvsPortConfig
	defaultCfg *ovsportv1alpha1.OvsPortConfig
}

// getConfig returns the port config for the given request name.
func (c *claimPortConfigs) getConfig(request string) *ovsportv1alpha1.OvsPortConfig {
	if cfg, ok := c.byRequest[request]; ok {
		return cfg
	}
	if c.defaultCfg != nil {
		return c.defaultCfg
	}
	return ovsportv1alpha1.DefaultOvsPortConfig()
}

// parseClaimConfigs extracts OvsPortConfig entries from the allocation config
// for the given driver, returning a claimPortConfigs
func parseClaimConfigs(configs []resourceapi.DeviceAllocationConfiguration) (*claimPortConfigs, error) {
	logger := klog.Background().WithName("parseClaimConfigs")

	result := &claimPortConfigs{
		byRequest: make(map[string]*ovsportv1alpha1.OvsPortConfig),
	}

	for _, cfg := range configs {
		var portConfig ovsportv1alpha1.OvsPortConfig

		if cfg.Opaque == nil || cfg.Opaque.Driver != consts.DriverName {
			continue
		}

		if cfg.Source == resourceapi.AllocationConfigSourceClass {
			logger.Info("Ignoring class-sourced config: not yet implemented")
			continue
		} else if cfg.Source != resourceapi.AllocationConfigSourceClaim {
			return nil, fmt.Errorf("unknown config source: %v", cfg.Source)
		}

		if err := json.Unmarshal(cfg.Opaque.Parameters.Raw, &portConfig); err != nil {
			return nil, fmt.Errorf("unmarshal OvsPortConfig: %w", err)
		}

		if portConfig.Kind != ovsportv1alpha1.KindOvsPortConfig {
			return nil, fmt.Errorf("unexpected kind %q in claim config: want %q", portConfig.Kind, ovsportv1alpha1.KindOvsPortConfig)
		}
		if portConfig.APIVersion != ovsportv1alpha1.APIVersion {
			return nil, fmt.Errorf("unexpected apiVersion %q in claim config: want %q", portConfig.APIVersion, ovsportv1alpha1.APIVersion)
		}

		if err := validatePortConfig(&portConfig); err != nil {
			return nil, fmt.Errorf("OvsPortConfig validation failed: %w", err)
		}

		// Empty Requests means "applies to all".
		if len(cfg.Requests) == 0 {
			result.defaultCfg = &portConfig
		} else {
			for _, req := range cfg.Requests {
				result.byRequest[req] = &portConfig
			}
		}
	}
	return result, nil
}

func validatePortConfig(config *ovsportv1alpha1.OvsPortConfig) error {
	if config.Vlan != nil && (*config.Vlan < 0 || *config.Vlan > 4095) {
		return fmt.Errorf("vlan %d out of range [0, 4095]", *config.Vlan)
	}
	if config.Policing != nil {
		if config.Policing.MaxRate == nil {
			return fmt.Errorf("policing.max_rate is required when policing is set")
		}
	}
	return nil
}
