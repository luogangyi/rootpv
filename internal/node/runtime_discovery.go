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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

const runtimeInventoryVersion = "rootpv-runtime-inventory-v1"

var criRuntimePluginNames = [...]string{"io.containerd.cri.v1.runtime", "io.containerd.grpc.v1.cri"}

type RuntimeDiscoveryOptions struct {
	NodeUID               string
	HostRoot              string
	MaxHandlers           int
	DefaultRuncCandidates []string
	WrapperPaths          []string
}

// DiscoverRuntimeInventory 只从 containerd 已配置的 CRI handler 收集运行时事实，
// 不扫描任意宿主机目录猜测厂商二进制，避免把非预期程序加入 RootPV 路由。
func DiscoverRuntimeInventory(configPath string, opts RuntimeDiscoveryOptions) (config.RuntimeInventory, error) {
	opts = opts.withDefaults()
	raw, data, err := loadContainerdConfig(configPath, opts.HostRoot)
	if err != nil {
		return config.RuntimeInventory{}, err
	}
	inventory := config.RuntimeInventory{
		NodeUID:                opts.NodeUID,
		ContainerdConfigDigest: sha256Hex(data),
	}
	discoverConfiguredHandlers(raw, opts, &inventory)
	appendDefaultRuncHandler(opts, &inventory)
	if len(inventory.Handlers) > opts.MaxHandlers {
		return config.RuntimeInventory{}, fmt.Errorf("runtime inventory exceeds max handlers %d", opts.MaxHandlers)
	}
	sort.Slice(inventory.Handlers, func(i, j int) bool { return inventory.Handlers[i].Name < inventory.Handlers[j].Name })
	return inventory, nil
}

// discoverConfiguredHandlers 仅遍历 containerd CRI 配置中的 handler，过滤 RootPV 自己生成的 shadow handler。
func discoverConfiguredHandlers(raw map[string]any, opts RuntimeDiscoveryOptions, inventory *config.RuntimeInventory) {
	for _, table := range criRuntimeTables(raw) {
		if inventory.DefaultHandler == "" {
			inventory.DefaultHandler = stringValue(table["default_runtime_name"])
		}
		for name, value := range mapValue(table["runtimes"]) {
			if config.IsRootPVShadowHandler(name) {
				continue
			}
			handler := discoverHandler(name, mapValue(value), opts)
			handler.Default = name == inventory.DefaultHandler
			inventory.Handlers = append(inventory.Handlers, handler)
		}
	}
}

// appendDefaultRuncHandler 兼容未显式声明 runtimes.runc 的标准 containerd 配置。
func appendDefaultRuncHandler(opts RuntimeDiscoveryOptions, inventory *config.RuntimeInventory) {
	if inventory.DefaultHandler != "runc" || inventoryHasHandler(*inventory, "runc") {
		return
	}
	handler := discoverHandler("runc", map[string]any{"runtime_type": config.RuncV2RuntimeType}, opts)
	handler.Default = true
	inventory.Handlers = append(inventory.Handlers, handler)
}

func inventoryHasHandler(inventory config.RuntimeInventory, name string) bool {
	for _, handler := range inventory.Handlers {
		if handler.Name == name {
			return true
		}
	}
	return false
}

func (opts RuntimeDiscoveryOptions) withDefaults() RuntimeDiscoveryOptions {
	if opts.MaxHandlers == 0 {
		opts.MaxHandlers = 64
	}
	if len(opts.DefaultRuncCandidates) == 0 {
		opts.DefaultRuncCandidates = []string{"/usr/local/sbin/runc", "/usr/bin/runc"}
	}
	if len(opts.WrapperPaths) == 0 {
		opts.WrapperPaths = []string{"/usr/local/bin/rootfs-runtime-wrapper", "/usr/local/bin/containerd-shim-rootpv-kata-v2"}
	}
	return opts
}

// discoverHandler 解析已支持 handler 的真实 delegate 路径，并生成节点解析摘要。
func discoverHandler(name string, table map[string]any, opts RuntimeDiscoveryOptions) config.DiscoveredRuntimeHandler {
	handler := config.DiscoveredRuntimeHandler{Name: name, RuntimeType: stringValue(table["runtime_type"])}
	if !config.SupportedRuntimeType(handler.RuntimeType) {
		handler.Reason = "unsupported_runtime_type"
		return handler
	}
	delegate := stringValue(mapValue(table["options"])["BinaryName"])
	source := runtimeHandlerSource{table: table}
	if handler.RuntimeType == config.RunscRuntimeType {
		snapshot, err := loadRunscConfig(table, opts.HostRoot)
		if err != nil {
			// Keep local paths and OS diagnostics out of the published inventory.
			handler.Reason, _, _ = strings.Cut(err.Error(), ":")
			return handler
		}
		delegate = snapshot.binary
		source.runsc = snapshot
	}
	if config.IsKataQemuRuntimeType(handler.RuntimeType) {
		delegate = stringValue(table["runtime_path"])
		if err := validateKataQemuConfig(table, opts.HostRoot); err != nil {
			handler.Reason = err.Error()
			return handler
		}
	}
	if delegate == "" && name == "runc" {
		for _, candidate := range opts.DefaultRuncCandidates {
			if executable(hostPath(opts.HostRoot, candidate)) {
				delegate = candidate
				break
			}
		}
	}
	if delegate == "" {
		handler.Reason = "delegate_unresolved"
		return handler
	}
	resolved, err := resolveSafeDelegate(delegate, opts)
	if err != nil {
		handler.Reason = err.Error()
		return handler
	}
	id, err := config.RuntimeAdapterID(name, handler.RuntimeType)
	if err != nil {
		handler.Reason = "adapter_id_invalid"
		return handler
	}
	digest, err := resolvedHandlerDigest(name, handler.RuntimeType, resolved, source, opts.HostRoot)
	if err != nil {
		handler.Reason = "runtime_digest_unavailable"
		return handler
	}
	handler.AdapterID, handler.Resolved, handler.NodeResolvedDigest = id, true, digest
	return handler
}

// resolveSafeDelegate 解析符号链接后检查可执行文件，并拒绝把 RootPV wrapper 自己当作 delegate。
func resolveSafeDelegate(delegate string, opts RuntimeDiscoveryOptions) (string, error) {
	if !filepath.IsAbs(delegate) {
		return "", fmt.Errorf("delegate_not_absolute")
	}
	resolved, err := filepath.EvalSymlinks(hostPath(opts.HostRoot, delegate))
	if err != nil {
		return "", fmt.Errorf("delegate_missing")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("delegate_not_executable")
	}
	for _, wrapper := range opts.WrapperPaths {
		wrapperResolved, err := filepath.EvalSymlinks(hostPath(opts.HostRoot, wrapper))
		if err == nil && wrapperResolved == resolved {
			return "", fmt.Errorf("delegate_is_rootpv_wrapper")
		}
	}
	return fromHostPath(opts.HostRoot, resolved), nil
}

func criRuntimeTables(raw map[string]any) []map[string]any {
	plugins := mapValue(raw["plugins"])
	var tables []map[string]any
	for _, name := range activeCRIRuntimePlugins(raw) {
		containerd := mapValue(mapValue(plugins[name])["containerd"])
		if len(containerd) > 0 {
			tables = append(tables, containerd)
		}
	}
	return tables
}

func activeCRIRuntimePlugins(raw map[string]any) []string {
	// This mapping supports config versions only through v3. Future versions
	// need explicit migration review; do not assume v3 semantics with >= 3.
	// Version 3 does not migrate the legacy io.containerd.grpc.v1.cri
	// runtime settings. Leave them on disk, but never discover their handlers.
	if raw["version"] == int64(3) {
		return criRuntimePluginNames[:1]
	}
	return criRuntimePluginNames[:]
}

func mapValue(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if result, ok := value.(map[string]any); ok {
		return result
	}
	if result, ok := value.(map[string]interface{}); ok {
		return result
	}
	return map[string]any{}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func hostPath(root, path string) string {
	if root == "" {
		return path
	}
	return filepath.Join(root, path)
}

func fromHostPath(root, path string) string {
	if root == "" {
		return path
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}
	if relative, err := filepath.Rel(root, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "/" + filepath.ToSlash(relative)
	}
	return path
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
