// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package node

import (
	"os"
	"path/filepath"
	"testing"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

func TestCRIOInventoryUsesEffectiveOCIHandlers(t *testing.T) {
	if !crioConfigValueMatches(false, nil) || crioConfigValueMatches(true, nil) {
		t.Fatal("effective omitted boolean comparison")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/bin/runc"), []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"runtime_type": "oci", "runtime_path": "/usr/bin/runc", "runtime_root": "/run/runc"}
	raw := map[string]any{"crio": map[string]any{"runtime": map[string]any{"default_runtime": "runc", "runtimes": map[string]any{
		"runc": source, "persistent-rootfs-old": source, "remote": map[string]any{"runtime_type": "vm"},
	}}}}
	inventory, err := discoverCRIOInventory(raw, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Handlers) != 2 || inventory.DefaultHandler != "runc" {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
	handler := inventory.Handlers[1]
	if !handler.Resolved || !handler.Default || handler.AdapterID != "runc-v2" {
		t.Fatalf("OCI route: %#v", handler)
	}
	if inventory.Handlers[0].Resolved {
		t.Fatal("unsupported task shim was advertised")
	}
	source["runtime_root"] = "/run/another-root"
	changed := discoverCRIOHandler("runc", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
	if changed.handler.NodeResolvedDigest == handler.NodeResolvedDigest {
		t.Fatal("runtime root change must invalidate node evidence")
	}
	catalog, err := config.BuildRuntimeCatalog([]config.RuntimeInventory{inventory, {DefaultHandler: "runc", Handlers: []config.DiscoveredRuntimeHandler{{Name: "runc", RuntimeType: config.RuncV2RuntimeType, Resolved: true}}}}, nil)
	if err != nil || len(catalog.Adapters) != 1 {
		t.Fatalf("mixed CRI OCI catalog: %#v, %v", catalog, err)
	}
}

func TestCRIORetainsCleanupHandler(t *testing.T) {
	root := t.TempDir()
	dropIn, registryPath := filepath.Join(root, "rootpv.conf"), filepath.Join(root, "registry.yaml")
	writeRuntimeConfigFile(t, dropIn, []byte("[crio.runtime.runtimes.persistent-rootfs-runc-v2]\nruntime_path = '/wrapper'\n"))
	writeRuntimeConfigFile(t, registryPath, []byte("formatVersion: rootpv-runtime-adapters-v2\nadapters:\n  runc-v2:\n    rootpvHandler: persistent-rootfs-runc-v2\n    delegateBinary: /runc\n"))
	registry := localAdapterRegistryV2{Adapters: map[string]localAdapterEntryV2{}}
	shadows, err := retainedCRIOShadows(dropIn, registryPath, &registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(shadows) != 1 || !registry.Adapters["runc-v2"].Retired {
		t.Fatalf("cleanup handler lost or still admits new creates: %#v, %#v", shadows, registry)
	}
	if registry.Adapters["runc-v2"].DelegateBinary != "/runc" {
		t.Fatal("retirement changed the delegate")
	}
}

func TestCRIODiscoveryRejectsNonRuncHandlers(t *testing.T) {
	for _, runtime := range []struct{ kind, binary, reason string }{
		{"vm", "/usr/bin/containerd-shim-kata-v2", "unsupported_crio_runtime_type"},
		{"vm", "/usr/bin/containerd-shim-runsc-v1", "unsupported_crio_runtime_type"},
		{"oci", "/usr/bin/crun", "unsupported_crio_oci_runtime: runc required"},
	} {
		table := map[string]any{"runtime_type": runtime.kind, "runtime_path": runtime.binary}
		_, err := loadCRIORuntimeSource(table)
		if err == nil || err.Error() != runtime.reason {
			t.Fatalf("%s: expected unsupported runtime, got %v", runtime.binary, err)
		}
	}
}
