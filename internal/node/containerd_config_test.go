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
	"reflect"
	"testing"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

func TestContainerdConfigFailsClosed(t *testing.T) {
	for _, body := range []string{
		"imports = ['missing.toml']", "imports = ['config.toml']",
		"[plugins.'io.containerd.grpc.v1.cri'.containerd]\ndefault_runtime_name = 'runc'\n[plugins.'io.containerd.cri.v1.runtime'.containerd]\ndefault_runtime_name = 'runc'\n",
	} {
		root := t.TempDir()
		path := filepath.Join(root, "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadContainerdConfig(path, ""); err == nil {
			t.Fatalf("accepted unsafe config: %s", body)
		}
	}
}

func TestContainerdImportsInitializeNilStack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeRuntimeConfigFile(t, path, []byte("version = 2\n"))
	raw, _, err := loadContainerdImports(path, "", nil, 0)
	if err != nil || raw == nil {
		t.Fatalf("config without imports: %#v %v", raw, err)
	}
}

func TestMergeConfigInitializesNilDestination(t *testing.T) {
	for _, dst := range []map[string]any{nil, {"plugins": map[string]any(nil)}} {
		src := map[string]any{"plugins": map[string]any{"runtime": "kata"}}
		merged := mergeConfig(dst, src)
		if !reflect.DeepEqual(merged, src) {
			t.Fatalf("merged: %#v", merged)
		}
	}
}

func TestStringSliceConfigHandlesUnsupportedValues(t *testing.T) {
	if got := stringSliceConfig(42); got != nil {
		t.Fatalf("unsupported value: %v", got)
	}
	if got := stringSliceConfig([]any{"kata.toml", 42}); !reflect.DeepEqual(got, []string{"kata.toml"}) {
		t.Fatalf("mixed imports: %v", got)
	}
}

func TestKataQemuConfigRequiresQemuHypervisor(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"missing hypervisor", "[runtime]\nhypervisor_name = 'qemu'\n", false},
		{"QEMU without runtime table", "[hypervisor.qemu]\npath = '/opt/kata/qemu'\n", true},
		{"QEMU runtime", "[hypervisor.qemu]\npath = '/opt/kata/qemu'\n[runtime]\nhypervisor_name = 'qemu'\n", true},
		{"conflicting runtime", "[hypervisor.qemu]\npath = '/opt/kata/qemu'\n[runtime]\nhypervisor_name = 'clh'\n", false},
		{"CLH", "[hypervisor.clh]\npath = '/opt/kata/clh'\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeRuntimeConfigFile(t, filepath.Join(root, "qemu.toml"), []byte(tc.body))
			table := map[string]any{"options": map[string]any{"ConfigPath": "/qemu.toml"}}
			if err := validateKataQemuConfig(table, root); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestResolvedHandlerDigestRejectsIncompleteEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "qemu.toml")
	table := map[string]any{"options": map[string]any{"ConfigPath": "/qemu.toml"}}
	if digest, err := resolvedHandlerDigest("kata", config.KataQemuRuntimeType, "/opt/kata/shim", runtimeHandlerSource{table: table}, root); err == nil || digest != "" {
		t.Fatalf("missing config accepted: %q %v", digest, err)
	}
	writeRuntimeConfigFile(t, path, []byte("[hypervisor.qemu]\npath = '/opt/kata/qemu'\n"))
	first, err := resolvedHandlerDigest("kata", config.KataQemuRuntimeType, "/opt/kata/shim", runtimeHandlerSource{table: table}, root)
	if err != nil || first == "" {
		t.Fatalf("valid digest: %q %v", first, err)
	}
	writeRuntimeConfigFile(t, path, []byte("[hypervisor.qemu]\npath = '/opt/kata/new-qemu'\n"))
	second, err := resolvedHandlerDigest("kata", config.KataQemuRuntimeType, "/opt/kata/shim", runtimeHandlerSource{table: table}, root)
	if err != nil || second == first {
		t.Fatalf("config change absent from digest: %q %v", second, err)
	}
	table["unsupported"] = make(chan int)
	if digest, err := resolvedHandlerDigest("kata", config.KataQemuRuntimeType, "/opt/kata/shim", runtimeHandlerSource{table: table}, root); err == nil || digest != "" {
		t.Fatalf("unencodable options accepted: %q %v", digest, err)
	}
}

func TestKataDigestFailureDoesNotPublishAdapter(t *testing.T) {
	root, path, _ := setupImportedKataRuntime(t)
	effective, _, err := loadContainerdConfig(path, root)
	if err != nil {
		t.Fatal(err)
	}
	source := adapterSource(effective, "kata-qemu")
	source["unsupported"] = make(chan int)
	handler := discoverHandler("kata-qemu", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
	if handler.Resolved || handler.NodeResolvedDigest != "" || handler.Reason != "runtime_digest_unavailable" {
		t.Fatalf("invalid digest published: %#v", handler)
	}
	raw := map[string]any{}
	adapter := config.CatalogRuntimeAdapter{ID: "kata-qemu-v2", SourceHandler: "kata-qemu", RuntimeType: config.KataQemuRuntimeType}
	if _, ready, err := reconcileCatalogAdapter(raw, effective, adapter, RuntimeReconcileOptions{HostRoot: root}); err == nil || ready || len(raw) != 0 {
		t.Fatalf("invalid digest installed: %#v, ready=%v, error=%v", raw, ready, err)
	}
}

func TestKataGenericShimRequiresExplicitQemuConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "qemu.toml")
	writeExecutable(t, root, "opt/kata/bin/shim")
	if err := os.WriteFile(path, []byte("[hypervisor.qemu]\npath='/opt/kata/qemu'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"runtime_type": "io.containerd.kata.v2", "runtime_path": "/opt/kata/bin/shim", "options": map[string]any{"ConfigPath": "/qemu.toml"}}
	handler := discoverHandler("kata", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
	if !handler.Resolved || handler.AdapterID != "kata-kata-qemu-v2" {
		t.Fatalf("generic QEMU shim: %#v", handler)
	}
	delete(source, "runtime_path")
	handler = discoverHandler("kata", source, (RuntimeDiscoveryOptions{HostRoot: root}).withDefaults())
	if handler.Resolved || handler.Reason != "delegate_unresolved" {
		t.Fatalf("unconfigured shim accepted: %#v", handler)
	}
}
