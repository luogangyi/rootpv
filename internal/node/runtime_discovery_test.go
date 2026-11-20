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
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

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
	// 隔离的 HostRoot 不含 runc 可执行文件：合成的隐式默认 handler 不随
	// 宿主机是否安装 runc 变化，inventory 保持可复现。
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(`
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.ascend]
  runtime_type = "io.containerd.runc.v2"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := DiscoverRuntimeInventory(path, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.DefaultHandler != "runc" {
		t.Fatalf("implicit containerd default must be reported: %#v", inventory)
	}
	var ascend config.DiscoveredRuntimeHandler
	for _, handler := range inventory.Handlers {
		if handler.Name == "ascend" {
			ascend = handler
		}
	}
	if ascend.Resolved || ascend.Reason != "delegate_unresolved" {
		t.Fatalf("vendor handler must fail closed without BinaryName: %#v", ascend)
	}
}

func TestDiscoverRuntimeInventoryFallsBackToImplicitRuncDefault(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "usr/bin/runc")
	writeExecutable(t, root, "usr/local/nvidia/toolkit/nvidia-container-runtime")
	configPath := filepath.Join(root, "etc/containerd/config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// RKE2 渲染的配置：default_runtime_name 缺省，但显式声明了 runc handler。
	if err := os.WriteFile(configPath, []byte(`
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nvidia]
  runtime_type = "io.containerd.runc.v2"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nvidia.options]
  BinaryName = "/usr/local/nvidia/toolkit/nvidia-container-runtime"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	inventory, err := DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.DefaultHandler != "runc" {
		t.Fatalf("implicit containerd default must be reported: %#v", inventory)
	}
	for _, handler := range inventory.Handlers {
		if want := handler.Name == "runc"; handler.Default != want {
			t.Fatalf("handler %q default flag = %v, want %v", handler.Name, handler.Default, want)
		}
	}
	// 隐式默认必须能点亮 node-default 路由，否则无 runtimeClass 的 RootPV Pod 无法准入。
	catalogWithNodeDefaultRoute(t, inventory)
}

func TestDiscoverRuntimeInventorySynthesizesImplicitDefaultRuncWithoutRuntimeTable(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "usr/bin/runc")
	path := filepath.Join(root, "config.toml")
	// 既无 default_runtime_name 也无 runtimes.runc：containerd 隐式 runc 默认。
	if err := os.WriteFile(path, []byte(`
[plugins.'io.containerd.grpc.v1.cri'.containerd]
  snapshotter = "overlayfs"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := DiscoverRuntimeInventory(path, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.DefaultHandler != "runc" {
		t.Fatalf("implicit default handler missing: %#v", inventory)
	}
	if len(inventory.Handlers) != 1 || inventory.Handlers[0].Name != "runc" || !inventory.Handlers[0].Resolved || !inventory.Handlers[0].Default {
		t.Fatalf("implicit default runc must be synthesized: %#v", inventory.Handlers)
	}
}

// 仅含 version 的最小配置是 containerd 接受的有效配置：CRI 以隐式 runc 默认运行。
// 从 inventory、catalog、准入到节点 reconcile 的整条链路必须与显式 runc 默认
// 同等对待，否则准入放行的 Pod 在节点上没有 shadow handler、无法启动。
// 各阶段断言拆分为下方独立的 helper，避免单个函数承担整条链路的复杂度。
func TestImplicitDefaultRuncReconcilesWithoutCRIRuntimeTable(t *testing.T) {
	for _, tc := range []struct {
		version string
		plugin  string
	}{
		// containerd 1.x（v2 配置）只认 legacy 插件名；2.x 会迁移到新名。
		{"2", "io.containerd.grpc.v1.cri"},
		{"3", "io.containerd.cri.v1.runtime"},
	} {
		t.Run("version "+tc.version, func(t *testing.T) {
			root, configPath := setupVersionOnlyContainerdConfig(t, tc.version)
			inventory := discoverImplicitDefaultRunc(t, configPath, root)
			catalog := catalogWithNodeDefaultRoute(t, inventory)
			reconcileImplicitDefaultRunc(t, configPath, root, catalog)
			assertImplicitRuncShadowHandler(t, configPath, root, tc.plugin)
		})
	}
}

// setupVersionOnlyContainerdConfig 写出仅含 version 的最小 containerd 配置，
// 并准备隐式 runc 默认所需的 runc 与 RootPV wrapper 可执行文件。
func setupVersionOnlyContainerdConfig(t *testing.T, version string) (string, string) {
	t.Helper()
	root := t.TempDir()
	writeExecutable(t, root, "usr/bin/runc")
	writeExecutable(t, root, "usr/local/bin/rootfs-runtime-wrapper")
	configPath := filepath.Join(root, "etc/containerd/config.toml")
	writeRuntimeConfigFile(t, configPath, []byte("version = "+version+"\n"))
	return root, configPath
}

// discoverImplicitDefaultRunc 断言最小配置按 containerd 隐式语义上报唯一的
// runc 默认 handler，并返回 inventory 供后续 catalog 链路复用。
func discoverImplicitDefaultRunc(t *testing.T, configPath, root string) config.RuntimeInventory {
	t.Helper()
	inventory, err := DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.DefaultHandler != "runc" || len(inventory.Handlers) != 1 {
		t.Fatalf("implicit default must be reported: %#v", inventory)
	}
	handler := inventory.Handlers[0]
	if handler.Name != "runc" || !handler.Default || !handler.Resolved || handler.AdapterID != "runc-v2" {
		t.Fatalf("implicit default runc handler: %#v", handler)
	}
	return inventory
}

// catalogWithNodeDefaultRoute 断言隐式默认能点亮 node-default 准入路由
// （无 runtimeClass 的 RootPV Pod 依赖该路由通过准入），并返回 catalog。
func catalogWithNodeDefaultRoute(t *testing.T, inventory config.RuntimeInventory) config.RuntimeCatalog {
	t.Helper()
	catalog, err := config.BuildRuntimeCatalog([]config.RuntimeInventory{inventory}, nil)
	if err != nil {
		t.Fatalf("BuildRuntimeCatalog: %v", err)
	}
	adapter, err := catalog.ResolveAdmissionAdapter("")
	if err != nil || adapter.RootfsRuntimeClass != "persistent-rootfs-node-default" {
		t.Fatalf("node-default route missing: adapter=%#v err=%v", adapter, err)
	}
	return catalog
}

// reconcileImplicitDefaultRunc 断言隐式默认 adapter 完成节点 reconcile：
// 只有准入路由而没有节点 readiness 时，被放行的 Pod 仍无法启动——
// shadow handler 必须真正落盘并发布就绪状态。
func reconcileImplicitDefaultRunc(t *testing.T, configPath, root string, catalog config.RuntimeCatalog) {
	t.Helper()
	result, err := ReconcileRuntimeAdapters(configPath, catalog, RuntimeReconcileOptions{
		HostRoot:      root,
		WrapperBinary: "/usr/local/bin/rootfs-runtime-wrapper",
		RegistryPath:  filepath.Join(root, "etc/rootpv/registry.yaml"),
	})
	if err != nil {
		t.Fatalf("ReconcileRuntimeAdapters: %v", err)
	}
	if result.Readiness["runc-v2"] == "" {
		t.Fatalf("implicit default adapter is not ready: %#v", result)
	}
}

// assertImplicitRuncShadowHandler 断言 shadow handler 物化在该 config 版本的
// 守护进程实际服务的插件下：v3（containerd 2.x）只有
// io.containerd.cri.v1.runtime；v2 在 containerd 1.x 上由 legacy
// io.containerd.grpc.v1.cri 直接生效，并被 containerd 2.x 迁移到新插件名。
func assertImplicitRuncShadowHandler(t *testing.T, configPath, root, plugin string) {
	t.Helper()
	runtimes := decodeRuntimeMapPlugin(t, configPath, plugin)
	shadow := mapValue(runtimes["persistent-rootfs-runc-v2"])
	if stringValue(shadow["runtime_type"]) != config.RuncV2RuntimeType {
		t.Fatalf("shadow handler was not installed under %s: %#v", plugin, shadow)
	}
	options := mapValue(shadow["options"])
	if stringValue(options["BinaryName"]) != "/usr/local/bin/rootfs-runtime-wrapper-runc-v2" {
		t.Fatalf("shadow handler delegate: %#v", options)
	}
	alias := filepath.Join(root, "usr/local/bin/rootfs-runtime-wrapper-runc-v2")
	if target, err := os.Readlink(alias); err != nil || target != "rootfs-runtime-wrapper" {
		t.Fatalf("generated wrapper alias: target=%q err=%v", target, err)
	}
}

// fail-closed 保护：runc 二进制缺失时，隐式默认 handler 保持 unresolved，
// 不得生成可用的 node-default 路由（准入必须继续拒绝）。
func TestImplicitDefaultRuncFailsClosedWithoutRuncBinary(t *testing.T) {
	root := t.TempDir() // 故意不放置 runc 可执行文件
	configPath := filepath.Join(root, "config.toml")
	writeRuntimeConfigFile(t, configPath, []byte("version = 3\n"))
	inventory, err := DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if inventory.DefaultHandler != "runc" || len(inventory.Handlers) != 1 {
		t.Fatalf("implicit default inventory: %#v", inventory)
	}
	if handler := inventory.Handlers[0]; handler.Name != "runc" || handler.Resolved || handler.Reason != "delegate_unresolved" {
		t.Fatalf("implicit default must fail closed without a runc binary: %#v", handler)
	}
	catalog, err := config.BuildRuntimeCatalog([]config.RuntimeInventory{inventory}, nil)
	if err != nil {
		t.Fatalf("BuildRuntimeCatalog: %v", err)
	}
	if _, err := catalog.ResolveAdmissionAdapter(""); err == nil {
		t.Fatal("implicit default must not admit pods while runc is unresolved")
	}
}

// 异构默认集群的既有聚合语义（appendDefaultRoute 仅在唯一默认时生成 node-default
// 路由）：部分节点显式以其它 handler 为默认、部分节点缺省回落隐式 runc 时，
// defaults 集合出现两个 handler，cluster-wide 的 "" 路由无法同时匹配两类节点，
// 因此不生成路由（fail-closed，不做猜测路由），显式 RuntimeClass 路由不受影响。
func TestHeterogeneousClusterDefaultsOmitNodeDefaultRoute(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "usr/bin/runc")
	writeExecutable(t, root, "usr/local/nvidia/toolkit/nvidia-container-runtime")
	implicitPath := filepath.Join(root, "implicit.toml")
	// 节点 A：default_runtime_name 缺省 → 隐式回落 runc 默认。
	writeRuntimeConfigFile(t, implicitPath, []byte(`
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nvidia]
  runtime_type = "io.containerd.runc.v2"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nvidia.options]
  BinaryName = "/usr/local/nvidia/toolkit/nvidia-container-runtime"
`))
	implicit, err := DiscoverRuntimeInventory(implicitPath, RuntimeDiscoveryOptions{NodeUID: "node-a", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if implicit.DefaultHandler != "runc" {
		t.Fatalf("implicit default handler missing: %#v", implicit)
	}
	explicitPath := filepath.Join(root, "explicit.toml")
	// 节点 B：显式 nvidia 默认。
	writeRuntimeConfigFile(t, explicitPath, []byte(`
[plugins.'io.containerd.cri.v1.runtime'.containerd]
  default_runtime_name = "nvidia"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nvidia]
  runtime_type = "io.containerd.runc.v2"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.nvidia.options]
  BinaryName = "/usr/local/nvidia/toolkit/nvidia-container-runtime"
`))
	explicit, err := DiscoverRuntimeInventory(explicitPath, RuntimeDiscoveryOptions{NodeUID: "node-b", HostRoot: root})
	if err != nil {
		t.Fatalf("DiscoverRuntimeInventory: %v", err)
	}
	if explicit.DefaultHandler != "nvidia" {
		t.Fatalf("explicit default handler missing: %#v", explicit)
	}
	catalog, err := config.BuildRuntimeCatalog(
		[]config.RuntimeInventory{implicit, explicit},
		[]config.RuntimeClassSource{{Name: "nvidia", Handler: "nvidia"}},
	)
	if err != nil {
		t.Fatalf("BuildRuntimeCatalog: %v", err)
	}
	if _, err := catalog.ResolveAdmissionAdapter(""); err == nil {
		t.Fatal("heterogeneous cluster defaults must not build a node-default route")
	}
	if _, err := catalog.ResolveAdmissionAdapter("nvidia"); err != nil {
		t.Fatalf("explicit runtime class route must survive heterogeneous defaults: %v", err)
	}
}

func TestDiscoverRuntimeInventoryMarksUnknownTaskShimUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.ascend]
  runtime_type = "io.containerd.runsc.v2"
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

func setupImportedKataRuntime(t *testing.T) (string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	writeExecutable(t, root, "opt/kata/runtime-rs/bin/containerd-shim-kata-v2")
	writeExecutable(t, root, "usr/local/bin/containerd-shim-rootpv-kata-v2")
	configPath := filepath.Join(root, "etc/containerd/config.toml")
	writeRuntimeConfigFile(t, configPath, []byte(`version = 2
imports = ["/etc/containerd/kata.toml"]
[plugins.'io.containerd.grpc.v1.cri'.containerd]
  default_runtime_name = "runc"
`))
	imported := []byte(`[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.kata-qemu]
 runtime_type = "io.containerd.kata-qemu.v2"
 runtime_path = "/opt/kata/runtime-rs/bin/containerd-shim-kata-v2"
 privileged_without_host_devices = true
 pod_annotations = ["io.katacontainers.*"]
[plugins.'io.containerd.grpc.v1.cri'.containerd.runtimes.kata-qemu.options]
 ConfigPath = "/etc/containerd/qemu.toml"
`)
	writeRuntimeConfigFile(t, filepath.Join(root, "etc/containerd/kata.toml"), imported)
	writeRuntimeConfigFile(t, filepath.Join(root, "etc/containerd/qemu.toml"), []byte("[hypervisor.qemu]\npath = '/opt/kata/qemu'\n"))
	return root, configPath, imported
}

func writeRuntimeConfigFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKataQemuDiscoveryAndReconcileImportedRuntime(t *testing.T) {
	root, configPath, imported := setupImportedKataRuntime(t)
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
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
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
	after, err := os.ReadFile(filepath.Join(root, "etc/containerd/kata.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(imported) {
		t.Fatal("vendor import changed")
	}
}

func TestKataQemuDiscoveryRejectsCLHConfig(t *testing.T) {
	root, configPath, _ := setupImportedKataRuntime(t)
	writeRuntimeConfigFile(t, filepath.Join(root, "etc/containerd/qemu.toml"), []byte("[hypervisor.clh]\npath = '/opt/kata/clh'\n"))
	inventory, err := DiscoverRuntimeInventory(configPath, RuntimeDiscoveryOptions{HostRoot: root, NodeUID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	for _, handler := range inventory.Handlers {
		if handler.Name == "kata-qemu" && (handler.Resolved || handler.Reason != "kata_hypervisor_not_qemu") {
			t.Fatalf("CLH accepted as QEMU: %#v", handler)
		}
	}
}
