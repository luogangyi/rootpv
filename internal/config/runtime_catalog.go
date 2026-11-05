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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	RuntimeCatalogFormatVersion = "rootpv-runtime-catalog-v1"
	RuntimeAdapterRegistryV2    = "rootpv-runtime-adapters-v2"
	maxGeneratedAdapterIDLength = 45

	RuntimeInventoryFormatVersion     = "rootpv-runtime-inventory-v1"
	RuntimeInventoryVersionAnnotation = "openfuyao.cn/rootpv-runtime-inventory-version"
	RuntimeInventoryAnnotation        = "openfuyao.cn/rootpv-runtime-inventory"
	RuntimeCatalogConfigMapName       = "rootpv-runtime-adapters"
	RuntimeCatalogDataKey             = "catalog.yaml"
	RuntimeClassOptOutAnnotation      = "openfuyao.cn/rootpv-auto-adapter"
	RuntimeCatalogManagedByLabel      = "openfuyao.cn/managed-by"
	RuntimeCatalogManagedByValue      = "rootpv"
	RuntimeAdapterIDLabel             = "openfuyao.cn/runtime-adapter-id"
)

// DiscoveredRuntimeHandler is the safe, path-free subset of a node's
// containerd runtime configuration. Delegate paths are intentionally kept out
// of the Kubernetes API and only live in the node-local registry.
type DiscoveredRuntimeHandler struct {
	Name               string `json:"name" yaml:"name"`
	RuntimeType        string `json:"runtimeType" yaml:"runtimeType"`
	AdapterID          string `json:"adapterID,omitempty" yaml:"adapterID,omitempty"`
	Resolved           bool   `json:"resolved" yaml:"resolved"`
	NodeResolvedDigest string `json:"nodeResolvedDigest,omitempty" yaml:"nodeResolvedDigest,omitempty"`
	Default            bool   `json:"default,omitempty" yaml:"default,omitempty"`
	Reason             string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

type RuntimeInventory struct {
	NodeUID                string                     `json:"nodeUID" yaml:"nodeUID"`
	ContainerdConfigDigest string                     `json:"containerdConfigDigest,omitempty" yaml:"containerdConfigDigest,omitempty"`
	DefaultHandler         string                     `json:"defaultHandler,omitempty" yaml:"defaultHandler,omitempty"`
	Handlers               []DiscoveredRuntimeHandler `json:"handlers" yaml:"handlers"`
}

// RuntimeClassSource is the controller's projection of an ordinary
// RuntimeClass. The controller keeps overhead/scheduling separately when it
// materializes a Shadow RuntimeClass.
type RuntimeClassSource struct {
	Name     string `json:"name" yaml:"name"`
	Handler  string `json:"handler" yaml:"handler"`
	OptedOut bool   `json:"optedOut,omitempty" yaml:"optedOut,omitempty"`
}

type RuntimeCatalog struct {
	FormatVersion string                  `json:"formatVersion" yaml:"formatVersion"`
	Generation    int64                   `json:"generation" yaml:"generation"`
	Adapters      []CatalogRuntimeAdapter `json:"adapters" yaml:"adapters"`
	Routes        []RuntimeAdapterRoute   `json:"routes" yaml:"routes"`
}

type CatalogRuntimeAdapter struct {
	ID            string `json:"id" yaml:"id"`
	SourceHandler string `json:"sourceHandler" yaml:"sourceHandler"`
	RuntimeType   string `json:"runtimeType" yaml:"runtimeType"`
	RootfsHandler string `json:"rootpvHandler" yaml:"rootpvHandler"`
	ClusterDigest string `json:"clusterDigest" yaml:"clusterDigest"`
}

type RuntimeAdapterRoute struct {
	OriginalRuntimeClass string `json:"originalRuntimeClass" yaml:"originalRuntimeClass"`
	AdapterID            string `json:"adapterID" yaml:"adapterID"`
	RootfsRuntimeClass   string `json:"rootpvRuntimeClass" yaml:"rootpvRuntimeClass"`
}

func (c RuntimeCatalog) Digest() (string, error) {
	payload := c
	payload.Generation = 0
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// RuntimeAdapterID returns a stable adapter identifier derived exclusively
// from the source handler and CRI runtime interface. It never contains a
// vendor binary path or a user-supplied adapter version.
func RuntimeAdapterID(handler, runtimeType string) (string, error) {
	if !SupportedRuntimeType(runtimeType) {
		return "", fmt.Errorf("unsupported runtime type %q", runtimeType)
	}
	token := normalizeRuntimeToken(handler)
	if token == "" {
		return "", fmt.Errorf("runtime handler is empty after normalization")
	}
	family := "runc-v2"
	base := "runc"
	switch {
	case IsKataQemuRuntimeType(runtimeType):
		family, base = "kata-qemu-v2", "kata-qemu"
	case runtimeType == RunscRuntimeType:
		family, base = "runsc-v1", "runsc"
	default:
		// All other supported types use the runc OCI shim family.
	}
	id := family
	if token != base {
		id = token + "-" + family
	}
	return limitGeneratedID(id, handler+"\x00"+runtimeType), nil
}

func IsRootPVShadowHandler(handler string) bool {
	return strings.HasPrefix(strings.ToLower(handler), "persistent-rootfs-")
}

func BuildRuntimeCatalog(inventories []RuntimeInventory, classes []RuntimeClassSource) (RuntimeCatalog, error) {
	reports := collectRuntimeReports(inventories)
	active, defaultHandlers := collectActiveHandlers(inventories, classes)
	adapters, err := buildCatalogAdapters(active, reports)
	if err != nil {
		return RuntimeCatalog{}, err
	}
	disambiguateCatalogAdapters(adapters)
	return buildCatalogRoutes(adapters, defaultHandlers, classes)
}

// collectRuntimeReports 按 handler 汇总节点上报，忽略 RootPV 自己生成的 shadow handler。
func collectRuntimeReports(inventories []RuntimeInventory) map[string][]DiscoveredRuntimeHandler {
	byHandler := map[string][]DiscoveredRuntimeHandler{}
	for _, inventory := range inventories {
		for _, handler := range inventory.Handlers {
			if handler.Name != "" && !IsRootPVShadowHandler(handler.Name) {
				byHandler[handler.Name] = append(byHandler[handler.Name], handler)
			}
		}
	}
	return byHandler
}

// collectActiveHandlers 结合 RuntimeClass 和节点默认 handler 确定需要生成 adapter 的运行时。
func collectActiveHandlers(inventories []RuntimeInventory, classes []RuntimeClassSource) (map[string]struct{}, map[string]struct{}) {
	active := map[string]struct{}{}
	for _, class := range classes {
		if class.Name != "" && class.Handler != "" && !class.OptedOut && !IsRootPVShadowHandler(class.Handler) {
			active[class.Handler] = struct{}{}
		}
	}
	defaults := map[string]struct{}{}
	for _, inventory := range inventories {
		if inventory.DefaultHandler != "" && !IsRootPVShadowHandler(inventory.DefaultHandler) {
			active[inventory.DefaultHandler] = struct{}{}
			defaults[inventory.DefaultHandler] = struct{}{}
		}
	}
	return active, defaults
}

// buildCatalogAdapters 仅为在所有节点上具有一致的受支持运行时证据的 handler 创建 adapter。
func buildCatalogAdapters(active map[string]struct{}, reports map[string][]DiscoveredRuntimeHandler) (map[string]CatalogRuntimeAdapter, error) {
	adapters := make(map[string]CatalogRuntimeAdapter, len(active))
	for handler := range active {
		runtimeType, resolved, err := resolveHandlerRuntimeType(handler, reports[handler])
		if err != nil {
			return nil, err
		}
		if SupportedRuntimeType(runtimeType) && resolved {
			id, err := RuntimeAdapterID(handler, runtimeType)
			if err != nil {
				return nil, err
			}
			adapter := CatalogRuntimeAdapter{ID: id, SourceHandler: handler, RuntimeType: runtimeType, RootfsHandler: "persistent-rootfs-" + id}
			adapter.ClusterDigest = catalogAdapterDigest(adapter)
			adapters[handler] = adapter
		}
	}
	return adapters, nil
}

func resolveHandlerRuntimeType(handler string, reports []DiscoveredRuntimeHandler) (string, bool, error) {
	if len(reports) == 0 {
		return "", false, nil
	}
	runtimeType := reports[0].RuntimeType
	resolved := false
	for _, report := range reports {
		if report.RuntimeType != runtimeType {
			return "", false, fmt.Errorf("runtime handler %q reports inconsistent runtime types %q and %q", handler, runtimeType, report.RuntimeType)
		}
		resolved = resolved || report.Resolved
	}
	return runtimeType, resolved, nil
}

// disambiguateCatalogAdapters 为归一化后发生 ID 冲突的 handler 加入确定性哈希后缀。
func disambiguateCatalogAdapters(adapters map[string]CatalogRuntimeAdapter) {
	if adapters == nil {
		return
	}
	byID := map[string][]string{}
	for handler, adapter := range adapters {
		byID[adapter.ID] = append(byID[adapter.ID], handler)
	}
	for _, handlers := range byID {
		if len(handlers) < 2 {
			continue
		}
		for _, handler := range handlers {
			adapter, ok := adapters[handler]
			if !ok {
				continue
			}
			identity := handler + "\x00" + adapter.RuntimeType
			adapter.ID = limitGeneratedID(adapter.ID+"-"+shortHash(identity), identity)
			adapter.RootfsHandler = "persistent-rootfs-" + adapter.ID
			adapter.ClusterDigest = catalogAdapterDigest(adapter)
			adapters[handler] = adapter
		}
	}
}

// buildCatalogRoutes 输出有序 adapter 列表和 RuntimeClass/默认 handler 路由。
func buildCatalogRoutes(adapters map[string]CatalogRuntimeAdapter, defaults map[string]struct{}, classes []RuntimeClassSource) (RuntimeCatalog, error) {
	catalog := RuntimeCatalog{FormatVersion: RuntimeCatalogFormatVersion}
	for _, adapter := range adapters {
		catalog.Adapters = append(catalog.Adapters, adapter)
	}
	sort.Slice(catalog.Adapters, func(i, j int) bool { return catalog.Adapters[i].ID < catalog.Adapters[j].ID })
	if err := appendRuntimeClassRoutes(&catalog, adapters, classes); err != nil {
		return RuntimeCatalog{}, err
	}
	appendDefaultRoute(&catalog, adapters, defaults)
	sort.Slice(catalog.Routes, func(i, j int) bool {
		return catalog.Routes[i].OriginalRuntimeClass < catalog.Routes[j].OriginalRuntimeClass
	})
	return catalog, nil
}

func appendRuntimeClassRoutes(catalog *RuntimeCatalog, adapters map[string]CatalogRuntimeAdapter, classes []RuntimeClassSource) error {
	seen := map[string]struct{}{}
	for _, class := range classes {
		if class.Name == "" || class.OptedOut || IsRootPVShadowHandler(class.Handler) {
			continue
		}
		adapter, ok := adapters[class.Handler]
		if !ok {
			continue
		}
		rootfsClass := "persistent-rootfs-" + limitGeneratedID(normalizeRuntimeToken(class.Name), class.Name)
		if _, exists := seen[rootfsClass]; exists {
			return fmt.Errorf("runtime class %q produces duplicate Shadow RuntimeClass %q", class.Name, rootfsClass)
		}
		seen[rootfsClass] = struct{}{}
		catalog.Routes = append(catalog.Routes, RuntimeAdapterRoute{OriginalRuntimeClass: class.Name, AdapterID: adapter.ID, RootfsRuntimeClass: rootfsClass})
	}
	return nil
}

func appendDefaultRoute(catalog *RuntimeCatalog, adapters map[string]CatalogRuntimeAdapter, defaults map[string]struct{}) {
	if len(defaults) != 1 {
		return
	}
	for handler := range defaults {
		if adapter, ok := adapters[handler]; ok {
			catalog.Routes = append(catalog.Routes, RuntimeAdapterRoute{AdapterID: adapter.ID, RootfsRuntimeClass: "persistent-rootfs-node-default"})
		}
	}
}

// ResolveAdmissionAdapter returns the route and digest evidence needed by
// admission. Delegate paths remain only in the node-local registry.
func (c RuntimeCatalog) ResolveAdmissionAdapter(originalRuntimeClass string) (RuntimeAdapter, error) {
	if c.FormatVersion != RuntimeCatalogFormatVersion {
		return RuntimeAdapter{}, fmt.Errorf("runtime catalog is unavailable")
	}
	for _, route := range c.Routes {
		if route.OriginalRuntimeClass != originalRuntimeClass {
			continue
		}
		for _, adapter := range c.Adapters {
			if adapter.ID == route.AdapterID {
				return RuntimeAdapter{ID: adapter.ID, Mode: RuntimeAdapterModeExplicit, OriginalRuntimeClass: originalRuntimeClass, OriginalHandler: adapter.SourceHandler, RootfsRuntimeClass: route.RootfsRuntimeClass, RootfsHandler: adapter.RootfsHandler, RuntimeType: adapter.RuntimeType, ConfigDigest: adapter.ClusterDigest}, nil
			}
		}
		return RuntimeAdapter{}, fmt.Errorf("runtime adapter %q is missing from catalog", route.AdapterID)
	}
	return RuntimeAdapter{}, fmt.Errorf("runtime adapter route not found for runtimeClass %q", originalRuntimeClass)
}

func catalogAdapterDigest(adapter CatalogRuntimeAdapter) string {
	payload := struct {
		FormatVersion string `json:"formatVersion"`
		ID            string `json:"id"`
		SourceHandler string `json:"sourceHandler"`
		RuntimeType   string `json:"runtimeType"`
		RootfsHandler string `json:"rootpvHandler"`
	}{
		FormatVersion: RuntimeCatalogFormatVersion,
		ID:            adapter.ID,
		SourceHandler: adapter.SourceHandler,
		RuntimeType:   adapter.RuntimeType,
		RootfsHandler: adapter.RootfsHandler,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeRuntimeToken(value string) string {
	var out []rune
	previousDash := true
	for _, r := range strings.ToLower(value) {
		if unicode.IsLower(r) || unicode.IsDigit(r) {
			out = append(out, r)
			previousDash = false
			continue
		}
		if !previousDash {
			out = append(out, '-')
			previousDash = true
		}
	}
	return strings.Trim(string(out), "-")
}

func limitGeneratedID(id, entropy string) string {
	if len(id) <= maxGeneratedAdapterIDLength {
		return id
	}
	suffix := "-" + shortHash(entropy)
	prefix := strings.Trim(id[:maxGeneratedAdapterIDLength-len(suffix)], "-")
	return prefix + suffix
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}
