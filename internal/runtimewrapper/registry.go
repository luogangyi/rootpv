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

package runtimewrapper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

type RegistryOptions struct {
	AllowedDelegateDirs []string
	WrapperBinary       string
}

type AdapterRegistry struct {
	FormatVersion     string                            `yaml:"formatVersion"`
	CatalogGeneration int64                             `yaml:"catalogGeneration,omitempty"`
	Adapters          map[string]ResolvedRuntimeAdapter `yaml:"adapters"`
}

type ResolvedRuntimeAdapter struct {
	Mode                 config.RuntimeAdapterMode `yaml:"mode"`
	SourceHandler        string                    `yaml:"sourceHandler"`
	RootfsRuntimeClass   string                    `yaml:"rootpvRuntimeClass"`
	RootfsHandler        string                    `yaml:"rootpvHandler"`
	RuntimeType          string                    `yaml:"runtimeType"`
	DelegateBinary       string                    `yaml:"delegateBinary"`
	ClusterAdapterDigest string                    `yaml:"clusterAdapterDigest"`
	ClusterDigest        string                    `yaml:"clusterDigest"`
	NodeResolvedDigest   string                    `yaml:"nodeResolvedDigest"`
}

// LoadAdapterRegistry 读取节点生成的受信任路由表，并验证 delegate 的路径、类型和版本证据。
// v2 注册表必须为 0600，避免低权限用户修改 wrapper 最终会 exec 的二进制路径。
func LoadAdapterRegistry(path string, opts RegistryOptions) (AdapterRegistry, error) {
	registry, err := readAdapterRegistry(path)
	if err != nil {
		return AdapterRegistry{}, err
	}
	if err := validateRegistryMode(path, registry.FormatVersion); err != nil {
		return AdapterRegistry{}, err
	}
	if err := normalizeRegistryAdapters(&registry, opts); err != nil {
		return AdapterRegistry{}, err
	}
	return registry, nil
}

// readAdapterRegistry 读取 YAML 并只校验注册表的整体结构。
func readAdapterRegistry(path string) (AdapterRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AdapterRegistry{}, err
	}
	var registry AdapterRegistry
	if err := yaml.Unmarshal(data, &registry); err != nil {
		return AdapterRegistry{}, fmt.Errorf("decode runtime adapter registry: %w", err)
	}
	if registry.FormatVersion != config.RuntimeAdapterFormatVersion && registry.FormatVersion != config.RuntimeAdapterRegistryV2 {
		return AdapterRegistry{}, fmt.Errorf("unsupported runtime adapter registry format %q", registry.FormatVersion)
	}
	if len(registry.Adapters) == 0 {
		return AdapterRegistry{}, fmt.Errorf("runtime adapter registry has no adapters")
	}
	return registry, nil
}

// validateRegistryMode 约束 v2 路由表的宿主机文件权限，避免 delegate 路径被低权限用户篡改。
func validateRegistryMode(path, formatVersion string) error {
	if formatVersion != config.RuntimeAdapterRegistryV2 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("runtime adapter registry v2 must be mode 0600")
	}
	return nil
}

// normalizeRegistryAdapters 填充版本兼容字段并逐条校验最终可执行的 delegate。
func normalizeRegistryAdapters(registry *AdapterRegistry, opts RegistryOptions) error {
	isV2 := registry.FormatVersion == config.RuntimeAdapterRegistryV2
	for id, adapter := range registry.Adapters {
		if isV2 {
			adapter.Mode = config.RuntimeAdapterModeExplicit
			adapter.ClusterAdapterDigest = adapter.ClusterDigest
		}
		delegate, err := normalizeRegistryAdapter(id, adapter, opts, isV2)
		if err != nil {
			return err
		}
		registry.Adapters[id] = delegate
	}
	return nil
}

func normalizeRegistryAdapter(id string, adapter ResolvedRuntimeAdapter, opts RegistryOptions, isV2 bool) (ResolvedRuntimeAdapter, error) {
	if id == "" || adapter.SourceHandler == "" || adapter.RootfsHandler == "" ||
		adapter.ClusterAdapterDigest == "" || adapter.NodeResolvedDigest == "" ||
		(!isV2 && adapter.RootfsRuntimeClass == "") {
		return ResolvedRuntimeAdapter{}, fmt.Errorf("runtime adapter registry entry %q has required fields missing", id)
	}
	if adapter.Mode != config.RuntimeAdapterModeExplicit && adapter.Mode != config.RuntimeAdapterModeNodeDefault {
		return ResolvedRuntimeAdapter{}, fmt.Errorf("runtime adapter registry entry %q has unsupported mode %q", id, adapter.Mode)
	}
	if adapter.RuntimeType != config.RuncV2RuntimeType {
		return ResolvedRuntimeAdapter{}, fmt.Errorf("runtime adapter registry entry %q has unsupported runtime type %q", id, adapter.RuntimeType)
	}
	delegate, err := validateDelegate(adapter.DelegateBinary, opts, isV2)
	if err != nil {
		return ResolvedRuntimeAdapter{}, fmt.Errorf("runtime adapter registry entry %q: %w", id, err)
	}
	adapter.DelegateBinary = delegate
	return adapter, nil
}

// Resolve 以 admission 写入的 ID、模式和集群摘要三元组匹配 adapter，任何不一致均失败关闭。
func (r AdapterRegistry) Resolve(id string, mode config.RuntimeAdapterMode, clusterDigest string) (ResolvedRuntimeAdapter, error) {
	adapter, ok := r.Adapters[id]
	if !ok {
		return ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_not_found")
	}
	if adapter.Mode != mode || adapter.ClusterAdapterDigest != clusterDigest {
		return ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_config_mismatch")
	}
	return adapter, nil
}

// ResolveByRootfsHandler resolves the generated handler used by a CRI sandbox.
// Sandbox OCI specs are created before the admission annotations reach the
// container config, so the immutable generated handler is the routing evidence
// available for the sandbox create operation.
func (r AdapterRegistry) ResolveByRootfsHandler(handler string) (string, ResolvedRuntimeAdapter, error) {
	if handler == "" {
		return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_not_found")
	}
	var (
		resolvedID string
		resolved   ResolvedRuntimeAdapter
	)
	for id, adapter := range r.Adapters {
		if adapter.RootfsHandler != handler {
			continue
		}
		if resolvedID != "" {
			return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_handler_ambiguous")
		}
		resolvedID = id
		resolved = adapter
	}
	if resolvedID == "" {
		return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_not_found")
	}
	return resolvedID, resolved, nil
}

// ResolveByWrapperBinary resolves a generated per-adapter wrapper alias. The
// alias is only used for sandbox operations where CRI omits the handler and
// admission annotations from the OCI spec.
func (r AdapterRegistry) ResolveByWrapperBinary(wrapperName string) (string, ResolvedRuntimeAdapter, error) {
	const prefix = "rootfs-runtime-wrapper-"
	id := strings.TrimPrefix(filepath.Base(wrapperName), prefix)
	if id == "" || id == filepath.Base(wrapperName) {
		return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_not_found")
	}
	adapter, ok := r.Adapters[id]
	if !ok {
		return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_not_found")
	}
	return id, adapter, nil
}

func validateDelegate(path string, opts RegistryOptions, trustedRegistry bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("delegate_binary_unsafe: path must be absolute")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("delegate_binary_unsafe: resolve path: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("delegate_binary_unsafe: executable regular file required")
	}
	if !trustedRegistry {
		allowed := false
		for _, dir := range opts.AllowedDelegateDirs {
			realDir, err := filepath.EvalSymlinks(dir)
			if err == nil && (realPath == realDir || strings.HasPrefix(realPath, realDir+string(filepath.Separator))) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("delegate_binary_unsafe: outside allowed directories")
		}
	}
	if opts.WrapperBinary != "" {
		wrapper, err := filepath.EvalSymlinks(opts.WrapperBinary)
		if err == nil && wrapper == realPath {
			return "", fmt.Errorf("delegate_binary_unsafe: delegate points to wrapper")
		}
	}
	return realPath, nil
}
