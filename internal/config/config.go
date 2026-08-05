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
	"regexp"
	"sort"
)

type Config struct {
	MutationVersion       string
	RuntimeMapping        RuntimeMapping
	RuntimeClassMode      RuntimeClassMode
	RuntimeAdapterCutover bool
	RuntimeAdapters       RuntimeAdapterConfig
	RuntimeCatalog        RuntimeCatalog
	StoragePolicy         StoragePolicy
}

type RuntimeClassMode string

const (
	RuntimeClassModeInject   RuntimeClassMode = "inject"
	RuntimeClassModePreserve RuntimeClassMode = "preserve"
)

type RuntimeMapping struct {
	DefaultRootfsRuntimeClass string
	Exact                     map[string]string
	Regex                     map[string]string
	ResourceHints             map[string]string
}

type StoragePolicy struct {
	Allowlist []string
	Denylist  []string
}

func Default() Config {
	return Config{
		MutationVersion:  "v1.2",
		RuntimeClassMode: RuntimeClassModeInject,
		RuntimeMapping: RuntimeMapping{
			DefaultRootfsRuntimeClass: "persistent-rootfs-runc",
			Exact: map[string]string{
				"":       "persistent-rootfs-runc",
				"runc":   "persistent-rootfs-runc",
				"nvidia": "persistent-rootfs-nvidia",
			},
			Regex: map[string]string{
				"^nvidia-.*$": "persistent-rootfs-nvidia",
			},
			ResourceHints: map[string]string{
				"nvidia.com/gpu": "persistent-rootfs-nvidia",
			},
		},
		StoragePolicy: StoragePolicy{
			Allowlist: []string{"local-nvme", "local-path", "fast-ssd", "ceph-rbd", "lvm-csi"},
			Denylist:  []string{"nfs", "nas", "cephfs", "glusterfs", "s3fs", "ossfs"},
		},
	}
}

func (m RuntimeMapping) Resolve(original string, resourceNames []string) (string, error) {
	if original != "" {
		if mapped, found, err := m.exactMapping(original); err != nil || found {
			return mapped, err
		}
		if mapped, found, err := m.regexMapping(original); err != nil || found {
			return mapped, err
		}
		return "", fmt.Errorf("runtimeClass %q has no persistent-rootfs mapping", original)
	}
	return m.defaultMapping(resourceNames)
}

func (m RuntimeMapping) exactMapping(original string) (string, bool, error) {
	if m.Exact == nil {
		return "", false, nil
	}
	mapped, found := m.Exact[original]
	if found && mapped == "" {
		return "", true, fmt.Errorf("runtimeClass %q maps to empty rootfs runtimeClass", original)
	}
	return mapped, found, nil
}

func (m RuntimeMapping) regexMapping(original string) (string, bool, error) {
	patterns := make([]string, 0, len(m.Regex))
	for pattern := range m.Regex {
		patterns = append(patterns, pattern)
	}
	sort.Strings(patterns)

	var matchedPattern string
	var matchedMapping string
	for _, pattern := range patterns {
		matcher, err := regexp.Compile(pattern)
		if err != nil {
			return "", false, fmt.Errorf("invalid runtimeClass regex %q: %w", pattern, err)
		}
		if !matcher.MatchString(original) {
			continue
		}

		mapped := m.Regex[pattern]
		if matchedPattern == "" {
			matchedPattern = pattern
			matchedMapping = mapped
			continue
		}
		// 重叠正则映射到不同目标时拒绝继续，避免 map 遍历顺序影响运行时选择。
		if mapped != matchedMapping {
			return "", false, fmt.Errorf("runtimeClass %q matches conflicting rootfs runtimeClass regexes %q and %q", original, matchedPattern, pattern)
		}
	}
	if matchedPattern == "" {
		return "", false, nil
	}
	if matchedMapping == "" {
		return "", true, fmt.Errorf("runtimeClass %q maps to empty rootfs runtimeClass", original)
	}
	return matchedMapping, true, nil
}

func (m RuntimeMapping) defaultMapping(resourceNames []string) (string, error) {
	for _, resourceName := range resourceNames {
		if mapped, ok := m.ResourceHints[resourceName]; ok && mapped != "" {
			return mapped, nil
		}
	}
	if m.Exact != nil {
		if mapped, ok := m.Exact[""]; ok && mapped != "" {
			return mapped, nil
		}
	}
	if m.DefaultRootfsRuntimeClass == "" {
		return "", fmt.Errorf("default persistent-rootfs runtimeClass is empty")
	}
	return m.DefaultRootfsRuntimeClass, nil
}
