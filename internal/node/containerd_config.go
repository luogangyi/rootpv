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

package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

// loadContainerdConfig follows explicit containerd imports (including globs)
// through the host filesystem. Source files are never rewritten by discovery.
func loadContainerdConfig(path, hostRoot string) (map[string]any, []byte, error) {
	return loadContainerdImports(path, hostRoot, map[string]bool{}, 0)
}

func loadContainerdImports(path, hostRoot string, stack map[string]bool, depth int) (map[string]any, []byte, error) {
	path = filepath.Clean(path)
	if stack == nil {
		stack = make(map[string]bool)
	}
	if depth > 16 || stack[path] {
		return nil, nil, fmt.Errorf("containerd imports cycle or depth exceeded: %s", path)
	}
	stack[path] = true
	defer delete(stack, path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw := make(map[string]any)
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, nil, fmt.Errorf("decode containerd config %s: %w", path, err)
	}
	evidence := append([]byte(nil), data...)
	imports, ok := raw["imports"]
	if !ok {
		return raw, evidence, nil
	}
	for _, pattern := range stringSliceConfig(imports) {
		if filepath.IsAbs(pattern) {
			pattern = hostPath(hostRoot, pattern)
		} else {
			pattern = filepath.Join(filepath.Dir(path), pattern)
		}
		matches := []string{pattern}
		var err error
		if strings.ContainsAny(pattern, "*?[") {
			matches, err = filepath.Glob(pattern)
		}
		if err != nil {
			return nil, nil, err
		}
		for _, imported := range matches {
			extra, content, err := loadContainerdImports(imported, hostRoot, stack, depth+1)
			if err != nil {
				return nil, nil, err
			}
			// Discovery only needs runtime plugin fields. Keep the entry point
			// identity and imports rather than projecting an imported root/state.
			for _, key := range []string{"version", "root", "state", "imports"} {
				delete(extra, key)
			}
			raw = mergeConfig(raw, extra)
			evidence = append(evidence, content...)
		}
	}
	return raw, evidence, nil
}

func stringSliceConfig(value any) []string {
	var result []string
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
	default:
		return nil
	}
	return result
}

func mergeConfig(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = make(map[string]any)
	}
	for key, value := range src {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := dst[key].(map[string]any); ok {
				dst[key] = mergeConfig(existing, nested)
				continue
			}
		}
		dst[key] = value
	}
	return dst
}

func validateKataQemuConfig(table map[string]any, hostRoot string) error {
	path := stringValue(mapValue(table["options"])["ConfigPath"])
	if !filepath.IsAbs(path) {
		return fmt.Errorf("kata_config_path_required")
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(hostPath(hostRoot, path), &raw); err != nil {
		return fmt.Errorf("kata_config_unreadable")
	}
	hypervisor, ok := raw["hypervisor"]
	if !ok {
		return fmt.Errorf("kata_hypervisor_not_qemu")
	}
	hypervisors := mapValue(hypervisor)
	if len(hypervisors) != 1 || len(mapValue(hypervisors["qemu"])) == 0 {
		return fmt.Errorf("kata_hypervisor_not_qemu")
	}
	runtime, ok := raw["runtime"]
	if !ok {
		return nil
	}
	if name := stringValue(mapValue(runtime)["hypervisor_name"]); name != "" && name != "qemu" {
		return fmt.Errorf("kata_hypervisor_not_qemu")
	}
	return nil
}

func resolvedHandlerDigest(name, runtimeType, delegate string, table map[string]any, hostRoot string) (string, error) {
	evidence := []byte(name + "\x00" + runtimeType + "\x00" + delegate)
	if config.IsKataQemuRuntimeType(runtimeType) {
		options, err := json.Marshal(table)
		if err != nil {
			return "", fmt.Errorf("encode Kata runtime options: %w", err)
		}
		evidence = append(evidence, options...)
		configPath := stringValue(mapValue(table["options"])["ConfigPath"])
		content, err := os.ReadFile(hostPath(hostRoot, configPath))
		if err != nil {
			return "", fmt.Errorf("read Kata config for digest: %w", err)
		}
		evidence = append(evidence, content...)
	}
	return sha256Hex(evidence), nil
}
