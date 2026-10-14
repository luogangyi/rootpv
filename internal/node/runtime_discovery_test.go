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
	"github.com/BurntSushi/toml"
	"os"
	"path/filepath"
	"testing"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

func TestDiscoverRuntimeInventoryFindsConfiguredRuncV2Handlers(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "usr/bin/runc")
	writeExecutable(t, root, "opt/ascend/bin/runtime")
	configPath := filepath.Join(root, "etc/containerd/config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`
[plugins.'io.containerd.cri.v1.runtime'.containerd]
  default_runtime_name = "runc"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.ascend]
  runtime_type = "io.containerd.runc.v2"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.ascend.options]
  BinaryName = "/opt/ascend/bin/runtime"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	inventory, err := DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.DefaultHandler != "runc" || len(inventory.Handlers) != 2 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
	got := map[string]config.DiscoveredRuntimeHandler{}
	for _, handler := range inventory.Handlers {
		got[handler.Name] = handler
	}
	if !got["runc"].Resolved || got["runc"].AdapterID != "runc-v2" {
		t.Fatalf("runc handler not resolved: %#v", got["runc"])
	}
	if !got["ascend"].Resolved || got["ascend"].AdapterID != "ascend-runc-v2" {
		t.Fatalf("ascend handler not resolved: %#v", got["ascend"])
	}
}

func TestDiscoverRuntimeInventoryFailsClosedWithoutVendorBinaryName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.ascend]
  runtime_type = "io.containerd.runc.v2"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := DiscoverRuntimeInventory(path, RuntimeDiscoveryOptions{NodeUID: "node-a"})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if len(inventory.Handlers) != 1 || inventory.Handlers[0].Resolved || inventory.Handlers[0].Reason != "delegate_unresolved" {
		t.Fatalf("vendor handler must fail closed without BinaryName: %#v", inventory.Handlers)
	}
}

func TestDiscoverRuntimeInventoryMarksNonRuncV2Unsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.ascend]
  runtime_type = "io.containerd.runsc.v1"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := DiscoverRuntimeInventory(path, RuntimeDiscoveryOptions{NodeUID: "node-a"})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.Handlers[0].Reason != "unsupported_runtime_type" || inventory.Handlers[0].Resolved {
		t.Fatalf("unexpected handler: %#v", inventory.Handlers[0])
	}
}

func TestDiscoverRuntimeInventorySynthesizesStockDefaultRuncWithoutRuntimeTable(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "usr/bin/runc")
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(`
[plugins.'io.containerd.grpc.v1.cri'.containerd]
  default_runtime_name = "runc"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := DiscoverRuntimeInventory(path, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if len(inventory.Handlers) != 1 || inventory.Handlers[0].Name != "runc" || !inventory.Handlers[0].Resolved || !inventory.Handlers[0].Default {
		t.Fatalf("stock default runc must be discovered: %#v", inventory)
	}
}

func writeExecutable(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestKataQemuDiscoveryAndReconcileImportedRuntime(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "opt/kata/runtime-rs/bin/containerd-shim-kata-v2")
	writeExecutable(t, root, "usr/local/bin/containerd-shim-rootpv-kata-v2")
	_ = os.MkdirAll(filepath.Join(root, "etc/containerd"), 0o755)
	configPath := filepath.Join(root, "etc/containerd/config.toml")
	main := []byte("version = 2\nimports = [\"/etc/containerd/kata.toml\"]\n[plugins.'io.containerd.grpc.v1.cri'.containerd]\n  default_runtime_name = \"runc\"\n")
	_ = os.WriteFile(configPath, main, 0o644)
	imported := []byte(`[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.kata-qemu]
 runtime_type = "io.containerd.kata-qemu.v2"
 runtime_path = "/opt/kata/runtime-rs/bin/containerd-shim-kata-v2"
 privileged_without_host_devices = true
 pod_annotations = ["io.katacontainers.*"]
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.kata-qemu.options]
 ConfigPath = "/etc/containerd/qemu.toml"
`)
	_ = os.WriteFile(filepath.Join(root, "etc/containerd/kata.toml"), imported, 0o644)
	_ = os.WriteFile(filepath.Join(root, "etc/containerd/qemu.toml"), []byte("[hypervisor.qemu]\npath = '/opt/kata/qemu'\n"), 0o644)
	inventory, err := DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{HostRoot: root, NodeUID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	var kata config.DiscoveredRuntimeHandler
	for _, handler := range inventory.Handlers {
		if handler.Name == "kata-qemu" {
			kata = handler
		}
	}
	if !kata.Resolved || kata.AdapterID != "kata-qemu-v2" {
		t.Fatalf("Kata unresolved: %#v", kata)
	}
	catalog, err := config.BuildRuntimeCatalog([]config.RuntimeInventory{inventory}, []config.RuntimeClassSource{{Name: "kata-qemu", Handler: "kata-qemu"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileRuntimeAdapters(configPath, catalog, RuntimeReconcileOptions{HostRoot: root, WrapperBinary: "/usr/local/bin/rootfs-runtime-wrapper", RegistryPath: filepath.Join(root, "etc/rootpv/registry.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Readiness["kata-qemu-v2"] == "" {
		t.Fatal("Kata not ready")
	}
	var raw map[string]any
	data, _ := os.ReadFile(configPath)
	_, err = toml.Decode(string(data), &raw)
	if err != nil {
		t.Fatal(err)
	}
	shadow := adapterSource(raw, "persistent-rootfs-kata-qemu-v2")
	if stringValue(shadow["runtime_path"]) != "/usr/local/bin/containerd-shim-rootpv-kata-qemu-v2" || stringValue(mapValue(shadow["options"])["ConfigPath"]) != "/etc/containerd/qemu.toml" {
		t.Fatalf("incorrect shim clone: %#v", shadow)
	}
	if _, ok := mapValue(shadow["options"])["BinaryName"]; ok {
		t.Fatal("Kata received OCI BinaryName")
	}
	after, _ := os.ReadFile(filepath.Join(root, "etc/containerd/kata.toml"))
	if string(after) != string(imported) {
		t.Fatal("vendor import changed")
	}
	_ = os.WriteFile(filepath.Join(root, "etc/containerd/qemu.toml"), []byte("[hypervisor.clh]\npath = '/opt/kata/clh'\n"), 0o644)
	inventory, err = DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{HostRoot: root, NodeUID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	for _, handler := range inventory.Handlers {
		if handler.Name == "kata-qemu" && (handler.Resolved || handler.Reason != "kata_hypervisor_not_qemu") {
			t.Fatalf("CLH accepted as QEMU: %#v", handler)
		}
	}
}
