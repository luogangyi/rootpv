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
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"gitcode.com/openFuyao/rootpv/internal/atomicfile"
	"gitcode.com/openFuyao/rootpv/internal/config"
)

const runtimeAdapterRegistryV2 = "rootpv-runtime-adapters-v2"

type RuntimeReconcileOptions struct {
	HostRoot       string
	WrapperBinary  string
	RegistryPath   string
	KataShimBinary string
	beforeWrite    func() error
	// CRI-O registry changes are read per invocation; they do not require
	// restarting the daemon when the runtime definitions remain unchanged.
	beforeRegistryWrite func() error
}

type RuntimeReconcileResult struct {
	Readiness map[string]string
	Changed   bool
	// Local diagnostics for reconcile logging; not published as node inventory.
	Unavailable map[string]string
}

type localAdapterRegistryV2 struct {
	FormatVersion     string                         `yaml:"formatVersion"`
	CatalogGeneration int64                          `yaml:"catalogGeneration"`
	Adapters          map[string]localAdapterEntryV2 `yaml:"adapters"`
}

type localAdapterEntryV2 struct {
	Retired            bool   `yaml:"retired,omitempty"`
	SourceHandler      string `yaml:"sourceHandler"`
	RootfsHandler      string `yaml:"rootpvHandler"`
	RuntimeType        string `yaml:"runtimeType"`
	DelegateBinary     string `yaml:"delegateBinary"`
	ClusterDigest      string `yaml:"clusterDigest"`
	NodeResolvedDigest string `yaml:"nodeResolvedDigest"`
}

// ReconcileRuntimeAdapters 从目录中的源 handler 克隆 RootPV shadow handler，
// runc 替换 BinaryName，runsc 生成独立 shim 配置，Kata QEMU 替换 runtime_path；
// 原厂 handler 始终不被改写。成功后同时写入仅 root 可读的本地 registry，调用方才可重载 containerd。
func ReconcileRuntimeAdapters(containerdConfig string, catalog config.RuntimeCatalog, opts RuntimeReconcileOptions) (RuntimeReconcileResult, error) {
	if catalog.FormatVersion != config.RuntimeCatalogFormatVersion {
		return RuntimeReconcileResult{}, fmt.Errorf("unsupported runtime catalog format %q", catalog.FormatVersion)
	}
	if opts.WrapperBinary == "" || opts.RegistryPath == "" {
		return RuntimeReconcileResult{}, fmt.Errorf("wrapper binary and registry path are required")
	}
	effective, _, err := loadContainerdConfig(containerdConfig, opts.HostRoot)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	data, err := os.ReadFile(containerdConfig)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return RuntimeReconcileResult{}, err
	}
	// The pre-catalog configurator installed these two fixed handlers.  They
	// are superseded by catalog-derived handlers (for example
	// persistent-rootfs-nvidia-runc-v2), and retaining them leaves an obsolete
	// annotation allowlist active after a namespace migration.
	removeLegacyRuntimeHandlers(raw)
	registry := localAdapterRegistryV2{FormatVersion: runtimeAdapterRegistryV2, CatalogGeneration: catalog.Generation, Adapters: map[string]localAdapterEntryV2{}}
	result := RuntimeReconcileResult{Readiness: map[string]string{}, Unavailable: map[string]string{}}
	beforeWrite := opts.beforeWrite
	opts.beforeWrite = func() error {
		result.Changed = true
		if beforeWrite != nil {
			return beforeWrite()
		}
		return nil
	}
	for _, adapter := range catalog.Adapters {
		entry, ok, err := reconcileCatalogAdapter(raw, effective, adapter, opts)
		if err != nil {
			if adapter.RuntimeType != config.RunscRuntimeType {
				return RuntimeReconcileResult{}, err
			}
			// raw retains the runsc handler; only legacy runc aliases are removed above.
			// Omit the failed adapter from the new registry to deny new creates.
			// Existing tasks keep their saved delegation route and immutable shim files.
			result.Unavailable[adapter.ID] = err.Error()
			continue
		}
		if !ok {
			continue
		}
		registry.Adapters[adapter.ID] = entry
		result.Readiness[adapter.ID] = readinessValue(adapter.ClusterDigest)
	}
	configChanged, err := writeTOMLWithHook(containerdConfig, data, raw, opts.beforeWrite)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	registryData, err := yaml.Marshal(registry)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	registryChanged, err := writeFileWithHook(opts.RegistryPath, registryData, 0o600, opts.beforeWrite)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	result.Changed = result.Changed || configChanged || registryChanged
	return result, nil
}

func reconcileCatalogAdapter(raw, effective map[string]any, adapter config.CatalogRuntimeAdapter, opts RuntimeReconcileOptions) (localAdapterEntryV2, bool, error) {
	if !config.SupportedRuntimeType(adapter.RuntimeType) {
		return localAdapterEntryV2{}, false, nil
	}
	source := adapterSource(effective, adapter.SourceHandler)
	if len(source) == 0 {
		if adapter.SourceHandler != "runc" {
			return localAdapterEntryV2{}, false, nil
		}
		source = map[string]any{"runtime_type": config.RuncV2RuntimeType}
	}
	if stringValue(source["runtime_type"]) != adapter.RuntimeType {
		return localAdapterEntryV2{}, false, nil
	}
	if config.IsKataQemuRuntimeType(adapter.RuntimeType) {
		if err := validateKataQemuConfig(source, opts.HostRoot); err != nil {
			return localAdapterEntryV2{}, false, nil
		}
	}
	handlerSource := runtimeHandlerSource{table: source}
	delegate := adapterDelegate(source, adapter.SourceHandler, opts.HostRoot)
	if adapter.RuntimeType == config.RunscRuntimeType {
		snapshot, err := loadRunscConfig(source, opts.HostRoot)
		if err != nil {
			return localAdapterEntryV2{}, false, err
		}
		handlerSource.runsc, delegate = snapshot, snapshot.binary
	}
	resolved, err := resolveSafeDelegate(delegate, RuntimeDiscoveryOptions{HostRoot: opts.HostRoot, WrapperPaths: []string{opts.WrapperBinary, kataShimBinary(opts)}}.withDefaults())
	if err != nil {
		return localAdapterEntryV2{}, false, nil
	}
	digest, err := resolvedHandlerDigest(adapter.SourceHandler, adapter.RuntimeType, resolved, handlerSource, opts.HostRoot)
	if err != nil {
		return localAdapterEntryV2{}, false, err
	}
	plugin, err := adapterRuntimePlugin(effective, adapter.SourceHandler)
	if err != nil {
		return localAdapterEntryV2{}, false, err
	}
	handler, err := buildAdapterHandler(adapter, handlerSource, opts)
	if err != nil {
		return localAdapterEntryV2{}, false, err
	}
	if err := installCRIRuntimeHandler(raw, plugin, adapter.RootfsHandler, handler); err != nil {
		return localAdapterEntryV2{}, false, err
	}
	return localAdapterEntryV2{SourceHandler: adapter.SourceHandler, RootfsHandler: adapter.RootfsHandler, RuntimeType: adapter.RuntimeType, DelegateBinary: resolved, ClusterDigest: adapter.ClusterDigest, NodeResolvedDigest: digest}, true, nil
}

// Select the source handler's runtime plugin, including imported tables.
// runc can use containerd's implicit default without an explicit handler.
// loadContainerdConfig rejects multiple runtime tables before this selection.
func adapterRuntimePlugin(effective map[string]any, sourceHandler string) (string, error) {
	plugins := mapValue(effective["plugins"])
	for _, name := range activeCRIRuntimePlugins(effective) {
		table := mapValue(mapValue(plugins[name])["containerd"])
		if len(table) == 0 {
			continue
		}
		if _, ok := mapValue(table["runtimes"])[sourceHandler]; ok {
			return name, nil
		}
	}
	if sourceHandler == "runc" {
		for _, name := range activeCRIRuntimePlugins(effective) {
			if len(mapValue(mapValue(plugins[name])["containerd"])) > 0 {
				return name, nil
			}
		}
		// A config that only sets the version is still valid: containerd then
		// serves CRI with its implicit runc default, materialized under the
		// canonical io.containerd.cri.v1.runtime plugin for both config v2
		// and v3 (v2 legacy grpc.v1.cri runtime settings migrate there too).
		// Materialize that table so the shadow handler is honored after reload.
		return criRuntimePluginNames[0], nil
	}
	return "", fmt.Errorf("containerd CRI runtime table is unavailable")
}

// Materialize only the selected runtime table after the handler is ready.
// Vendor settings remain in imports; unrelated plugins are never expanded.
func installCRIRuntimeHandler(raw map[string]any, name, handlerName string, handler map[string]any) error {
	if raw == nil {
		return fmt.Errorf("containerd config is nil")
	}
	plugins := mapValue(raw["plugins"])
	plugin := mapValue(plugins[name])
	table := mapValue(plugin["containerd"])
	runtimes := mapValue(table["runtimes"])
	runtimes[handlerName] = handler
	table["runtimes"] = runtimes
	plugin["containerd"] = table
	plugins[name] = plugin
	raw["plugins"] = plugins
	return nil
}

func adapterSource(raw map[string]any, sourceHandler string) map[string]any {
	for _, table := range criRuntimeTables(raw) {
		if value, ok := mapValue(table["runtimes"])[sourceHandler]; ok {
			return mapValue(value)
		}
	}
	return nil
}

func adapterDelegate(source map[string]any, sourceHandler, hostRoot string) string {
	if config.IsKataQemuRuntimeType(stringValue(source["runtime_type"])) {
		return stringValue(source["runtime_path"])
	}
	delegate := stringValue(mapValue(source["options"])["BinaryName"])
	if delegate != "" || sourceHandler != "runc" {
		return delegate
	}
	for _, candidate := range (RuntimeDiscoveryOptions{HostRoot: hostRoot}).withDefaults().DefaultRuncCandidates {
		if executable(hostPath(hostRoot, candidate)) {
			return candidate
		}
	}
	return ""
}

func buildAdapterHandler(adapter config.CatalogRuntimeAdapter, source runtimeHandlerSource, opts RuntimeReconcileOptions) (map[string]any, error) {
	aliasOpts := opts
	if config.IsKataQemuRuntimeType(adapter.RuntimeType) {
		aliasOpts.WrapperBinary = kataShimBinary(opts)
		if !executable(hostPath(opts.HostRoot, aliasOpts.WrapperBinary)) {
			return nil, fmt.Errorf("Kata proxy shim is not installed: %s", aliasOpts.WrapperBinary)
		}
	}
	wrapperBinary, err := ensureAdapterWrapper(aliasOpts, adapter.ID)
	if err != nil {
		return nil, err
	}
	if adapter.RuntimeType == config.RunscRuntimeType {
		return buildRunscHandler(adapter, source, opts, wrapperBinary)
	}
	clone := cloneMap(source.table)
	clone["pod_annotations"] = appendStringValue(clone["pod_annotations"], "openfuyao.cn/*")
	clone["container_annotations"] = appendStringValue(clone["container_annotations"], "openfuyao.cn/*")
	if config.IsKataQemuRuntimeType(adapter.RuntimeType) {
		clone["runtime_path"] = wrapperBinary
	} else {
		options := mapValue(clone["options"])
		options["BinaryName"] = wrapperBinary
		clone["options"] = options
	}
	return clone, nil
}

func removeLegacyRuntimeHandlers(raw map[string]any) {
	for _, table := range criRuntimeTables(raw) {
		runtimes := mapValue(table["runtimes"])
		if len(runtimes) == 0 {
			continue
		}
		delete(runtimes, "persistent-rootfs-runc")
		delete(runtimes, "persistent-rootfs-nvidia")
		table["runtimes"] = runtimes
	}
}

func appendStringValue(value any, required string) []string {
	seen := map[string]bool{}
	var result []string
	appendValue := func(value string) {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	switch values := value.(type) {
	case []string:
		for _, value := range values {
			appendValue(value)
		}
	case []any:
		for _, value := range values {
			appendValue(stringValue(value))
		}
	default:
		// 配置可能省略该字段或使用不支持的类型；忽略它并仅写入必需项。
	}
	appendValue(required)
	return result
}

// ensureAdapterWrapper creates a generated, per-adapter alias to the single
// wrapper binary. CRI does not pass its selected runtime handler into a
// sandbox OCI spec, so the alias is the only immutable execution identity the
// wrapper receives for sandbox create/delete operations.
func ensureAdapterWrapper(opts RuntimeReconcileOptions, adapterID string) (string, error) {
	if adapterID == "" || filepath.Base(adapterID) != adapterID || adapterID == "." || adapterID == ".." {
		return "", fmt.Errorf("invalid runtime adapter ID")
	}
	alias := adapterWrapperBinary(opts.WrapperBinary, adapterID)
	target := hostPath(opts.HostRoot, alias)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if existing, readErr := os.Readlink(target); readErr == nil && existing == filepath.Base(opts.WrapperBinary) {
				return alias, nil
			}
		}
		return "", fmt.Errorf("generated adapter wrapper %q already exists and is not managed", alias)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Symlink(filepath.Base(opts.WrapperBinary), target); err != nil {
		return "", err
	}
	return alias, nil
}

func adapterWrapperBinary(wrapperBinary, adapterID string) string {
	if filepath.Base(wrapperBinary) == "containerd-shim-rootpv-kata-v2" {
		return filepath.Join(filepath.Dir(wrapperBinary), "containerd-shim-rootpv-"+adapterID)
	}
	return filepath.Join(filepath.Dir(wrapperBinary), "rootfs-runtime-wrapper-"+adapterID)
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []map[string]any:
			copyValues := make([]map[string]any, 0, len(typed))
			for _, item := range typed {
				copyValues = append(copyValues, cloneMap(item))
			}
			result[key] = copyValues
		default:
			result[key] = value
		}
	}
	return result
}

func writeTOMLIfChanged(path string, previous []byte, raw map[string]any) (bool, error) {
	return writeTOMLWithHook(path, previous, raw, nil)
}

func writeTOMLWithHook(path string, previous []byte, raw map[string]any, beforeWrite func() error) (bool, error) {
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(raw); err != nil {
		return false, err
	}
	if _, err := toml.Decode(encoded.String(), &map[string]any{}); err != nil {
		return false, fmt.Errorf("validate rendered containerd config: %w", err)
	}
	if bytes.Equal(previous, encoded.Bytes()) {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return writeFileWithHook(path, encoded.Bytes(), info.Mode().Perm(), beforeWrite)
}

func writeFileIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	return writeFileWithHook(path, data, mode, nil)
}

func writeFileWithHook(path string, data []byte, mode os.FileMode, beforeWrite func() error) (bool, error) {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return false, nil
	}
	if beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			return false, err
		}
	}
	return true, writeFileAtomicMode(path, data, mode)
}

func writeFileAtomicMode(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(path+".rootpv.tmp", path, data, mode)
}

func readinessValue(digest string) string {
	if len(digest) > len("sha256:")+16 {
		return digest[len("sha256:") : len("sha256:")+16]
	}
	return digest
}

func kataShimBinary(opts RuntimeReconcileOptions) string {
	if opts.KataShimBinary != "" {
		return opts.KataShimBinary
	}
	return filepath.Join(filepath.Dir(opts.WrapperBinary), "containerd-shim-rootpv-kata-v2")
}
