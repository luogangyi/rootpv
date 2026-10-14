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
)

func TestContainerdImportsFailClosedForMissingExplicitFileAndCycles(t *testing.T) {
	for _, body := range []string{"imports = ['missing.toml']", "imports = ['config.toml']"} {
		root := t.TempDir()
		path := filepath.Join(root, "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadContainerdConfig(path, ""); err == nil {
			t.Fatalf("accepted broken imports: %s", body)
		}
	}
}

func TestKataGenericShimRequiresExplicitQemuConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "qemu.toml")
	writeExecutable(t, root, "opt/kata/bin/shim")
	_ = os.WriteFile(path, []byte("[hypervisor.qemu]\npath='/opt/kata/qemu'\n"), 0o644)
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
