// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/crio"
)

func crioRuntimeTable(raw map[string]any) map[string]any {
	return mapValue(mapValue(raw["crio"])["runtime"])
}

func DiscoverCRIOInventory(ctx context.Context, socket string, opts RuntimeDiscoveryOptions) (config.RuntimeInventory, error) {
	raw, err := (crio.Client{Socket: socket}).Config(ctx)
	if err != nil {
		return config.RuntimeInventory{}, err
	}
	if err := crio.ValidateStorageSafety(raw); err != nil {
		return config.RuntimeInventory{}, err
	}
	return discoverCRIOInventory(raw, opts.withDefaults())
}

func discoverCRIOInventory(raw map[string]any, opts RuntimeDiscoveryOptions) (config.RuntimeInventory, error) {
	runtime := crioRuntimeTable(raw)
	inventory := config.RuntimeInventory{NodeUID: opts.NodeUID, RuntimeProvider: crio.Provider, DefaultHandler: stringValue(runtime["default_runtime"])}
	for name, value := range mapValue(runtime["runtimes"]) {
		if config.IsRootPVShadowHandler(name) {
			continue
		}
		resolved := discoverCRIOHandler(name, mapValue(value), opts)
		handler := resolved.handler
		handler.Default = name == inventory.DefaultHandler
		inventory.Handlers = append(inventory.Handlers, handler)
	}
	if len(inventory.Handlers) > opts.MaxHandlers {
		return inventory, fmt.Errorf("runtime inventory exceeds max handlers")
	}
	sort.Slice(inventory.Handlers, func(i, j int) bool { return inventory.Handlers[i].Name < inventory.Handlers[j].Name })
	data, err := json.Marshal(inventory.Handlers)
	if err != nil {
		return inventory, err
	}
	inventory.RuntimeConfigDigest = sha256Hex(data)
	return inventory, nil
}

// The OCI CLI contract uses the existing OCI family in catalog routes; the
// node-local provider decides how handlers and storage are installed. This
// lets one RuntimeClass target compatible containerd and CRI-O nodes.
type crioResolvedHandler struct {
	handler  config.DiscoveredRuntimeHandler
	delegate string
}

func discoverCRIOHandler(name string, table map[string]any, opts RuntimeDiscoveryOptions) crioResolvedHandler {
	result := crioResolvedHandler{handler: config.DiscoveredRuntimeHandler{Name: name}}
	source, err := loadCRIORuntimeSource(table)
	if err != nil {
		result.handler.Reason = err.Error()
		return result
	}
	result.handler.RuntimeType = source.runtimeType
	delegate, err := resolveSafeDelegate(source.delegate, opts)
	if err != nil {
		result.handler.Reason = "delegate_unresolved"
		return result
	}
	id, err := config.RuntimeAdapterID(name, source.runtimeType)
	if err != nil {
		result.handler.Reason = "adapter_id_invalid"
		return result
	}
	data, err := json.Marshal(table)
	if err != nil {
		result.handler.Reason = "runtime_digest_unavailable"
		return result
	}
	evidence := append([]byte(crio.Provider+"\x00"+delegate+"\x00"), data...)
	result.handler.AdapterID = id
	result.handler.NodeResolvedDigest = sha256Hex(evidence)
	result.handler.Resolved = true
	result.delegate = delegate
	return result
}

// ReconcileCRIOAdapters writes only a dedicated drop-in and the node registry.
// Source/vendor definitions are never edited. The caller restarts CRI-O and
// verifies the effective handler before publishing readiness.
func ReconcileCRIOAdapters(ctx context.Context, socket, dropIn string, catalog config.RuntimeCatalog, opts RuntimeReconcileOptions) (RuntimeReconcileResult, error) {
	if catalog.FormatVersion != config.RuntimeCatalogFormatVersion || opts.RegistryPath == "" || opts.WrapperBinary == "" {
		return RuntimeReconcileResult{}, fmt.Errorf("catalog, wrapper and registry are required")
	}
	raw, err := (crio.Client{Socket: socket}).Config(ctx)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	if err := crio.ValidateStorageSafety(raw); err != nil {
		return RuntimeReconcileResult{}, err
	}
	runtimes := mapValue(crioRuntimeTable(raw)["runtimes"])
	registry := localAdapterRegistryV2{FormatVersion: runtimeAdapterRegistryV2, CatalogGeneration: catalog.Generation, Adapters: map[string]localAdapterEntryV2{}}
	shadows, err := retainedCRIOShadows(dropIn, opts.RegistryPath, &registry)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	result := RuntimeReconcileResult{Readiness: map[string]string{}, Unavailable: map[string]string{}}
	for _, adapter := range catalog.Adapters {
		source := mapValue(runtimes[adapter.SourceHandler])
		resolved := discoverCRIOHandler(adapter.SourceHandler, source, RuntimeDiscoveryOptions{HostRoot: opts.HostRoot, WrapperPaths: []string{opts.WrapperBinary}}.withDefaults())
		handler := resolved.handler
		if !handler.Resolved || handler.RuntimeType != adapter.RuntimeType {
			result.Unavailable[adapter.ID] = handler.Reason
			continue
		}
		shadow, err := buildCRIOShadow(adapter, source, opts)
		if err != nil {
			result.Unavailable[adapter.ID] = err.Error()
			continue
		}
		shadows[adapter.RootfsHandler] = shadow
		registry.Adapters[adapter.ID] = localAdapterEntryV2{SourceHandler: adapter.SourceHandler, RootfsHandler: adapter.RootfsHandler, RuntimeType: adapter.RuntimeType, DelegateBinary: resolved.delegate, ClusterDigest: adapter.ClusterDigest, NodeResolvedDigest: handler.NodeResolvedDigest}
		result.Readiness[adapter.ID] = readinessValue(adapter.ClusterDigest)
	}
	result.Changed, err = writeCRIOAdapters(dropIn, registry, shadows, opts)
	return result, err
}

func writeCRIOAdapters(dropIn string, registry localAdapterRegistryV2, shadows map[string]any, opts RuntimeReconcileOptions) (bool, error) {
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(map[string]any{"crio": map[string]any{"runtime": map[string]any{"runtimes": shadows}}}); err != nil {
		return false, err
	}
	data, err := yaml.Marshal(registry)
	if err != nil {
		return false, err
	}
	// Runtime validation executes wrapper --version/features during reload.
	// Therefore its root-owned registry must be available before the drop-in.
	registryChanged, err := writeFileWithHook(opts.RegistryPath, data, 0o600, opts.beforeRegistryWrite)
	if err != nil {
		return false, err
	}
	changed, err := writeFileWithHook(dropIn, encoded.Bytes(), 0o600, opts.beforeWrite)
	return changed || registryChanged, err
}

// A removed/unavailable source must not remove a running container's handler
// from CRI-O's runtime map. Retired entries serve probes and saved cleanup
// routes, but reject new creates. Files are removed only after an operator
// drains the runtime; a missing CRI record alone cannot prove no create is in flight.
func retainedCRIOShadows(dropIn, registryPath string, registry *localAdapterRegistryV2) (map[string]any, error) {
	data, err := os.ReadFile(dropIn)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var previous map[string]any
	if _, err := toml.Decode(string(data), &previous); err != nil {
		return nil, err
	}
	shadows := mapValue(crioRuntimeTable(previous)["runtimes"])
	data, err = os.ReadFile(registryPath)
	if err != nil {
		return nil, err
	}
	var old localAdapterRegistryV2
	if err := yaml.Unmarshal(data, &old); err != nil {
		return nil, err
	}
	if old.FormatVersion != runtimeAdapterRegistryV2 {
		return nil, fmt.Errorf("invalid previous CRI-O adapter registry")
	}
	retained := map[string]any{}
	for id, entry := range old.Adapters {
		if shadow, ok := shadows[entry.RootfsHandler]; ok {
			entry.Retired = true
			registry.Adapters[id] = entry
			retained[entry.RootfsHandler] = shadow
		}
	}
	return retained, nil
}

func cloneConfigMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func verifyCRIOHandlers(ctx context.Context, opts RuntimeAdapterReconcilerOptions) error {
	raw, err := (crio.Client{Socket: opts.CRIOSocket}).Config(ctx)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(opts.CRIODropIn)
	if err != nil {
		return err
	}
	var expected map[string]any
	if _, err := toml.Decode(string(data), &expected); err != nil {
		return err
	}
	actual := mapValue(crioRuntimeTable(raw)["runtimes"])
	for name, value := range mapValue(crioRuntimeTable(expected)["runtimes"]) {
		want, got := mapValue(value), mapValue(actual[name])
		for key, value := range want {
			if !crioConfigValueMatches(value, got[key]) {
				return fmt.Errorf("CRI-O handler %q has not activated %q", name, key)
			}
		}
	}
	return nil
}

// CRI-O omits false booleans when serializing its effective configuration.
func crioConfigValueMatches(want, got any) bool {
	if flag, ok := want.(bool); ok && !flag && got == nil {
		return true
	}
	left, leftErr := json.Marshal(want)
	right, rightErr := json.Marshal(got)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}
