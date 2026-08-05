/*
 * Copyright (c) 2026 OpenFuyao Contributors.
 * RootPV is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 * http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadAdmissionConfig(runtimeAdaptersPath string, cutover bool) (Config, error) {
	cfg := Default()
	cfg.RuntimeAdapterCutover = cutover
	if !cutover {
		return cfg, nil
	}
	if runtimeAdaptersPath == "" {
		return Config{}, fmt.Errorf("runtime adapter configuration path is required during cutover")
	}
	data, err := os.ReadFile(runtimeAdaptersPath)
	if err != nil {
		return Config{}, fmt.Errorf("read runtime adapter configuration: %w", err)
	}
	var adapters RuntimeAdapterConfig
	if err := yaml.Unmarshal(data, &adapters); err != nil {
		return Config{}, fmt.Errorf("decode runtime adapter configuration: %w", err)
	}
	if err := adapters.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate runtime adapter configuration: %w", err)
	}
	cfg.RuntimeAdapters = adapters
	return cfg, nil
}
