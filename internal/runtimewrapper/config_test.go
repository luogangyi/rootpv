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
	"os"
	"path/filepath"
	"testing"
)

func TestConfigSelectsDelegateByWrapperName(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.OverlayMode != OverlayModeNestedRootfs {
		t.Fatalf("flat mode must not be enabled implicitly, got %q", cfg.OverlayMode)
	}
	if cfg.ContainerdAddress != "/run/containerd/containerd.sock" || cfg.ContainerdNamespace != "k8s.io" {
		t.Fatalf("unexpected containerd defaults: %+v", cfg)
	}

	runtime, err := cfg.DelegateRuntime("rootfs-runtime-wrapper-nvidia")
	if err != nil {
		t.Fatalf("DelegateRuntime: %v", err)
	}
	if runtime != "/usr/bin/nvidia-container-runtime" {
		t.Fatalf("unexpected nvidia delegate: %q", runtime)
	}

	runtime, err = cfg.DelegateRuntime("rootfs-runtime-wrapper-runc")
	if err != nil {
		t.Fatalf("DelegateRuntime: %v", err)
	}
	if runtime != "/usr/bin/runc" {
		t.Fatalf("unexpected runc delegate: %q", runtime)
	}
}

func TestLoadConfigOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rootfs-runtime-wrapper.yaml")
	if err := os.WriteFile(path, []byte(`
defaultDelegateRuntime: /custom/runc
rootfsdSocket: /tmp/rootfsd.sock
runtimeRoot: /tmp/rootpv-runtime
overlayMode: flat-lowerdirs
containerdAddress: /tmp/containerd.sock
containerdNamespace: test-ns
allowedOverlaySnapshotRoots:
  - /tmp/containerd/snapshots
delegateRuntimeByWrapper:
  custom-wrapper: /custom/runtime
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.RootfsdSocket != "/tmp/rootfsd.sock" || cfg.RuntimeRoot != "/tmp/rootpv-runtime" {
		t.Fatalf("unexpected config paths: %+v", cfg)
	}
	if cfg.OverlayMode != OverlayModeFlatLowerdirs || cfg.ContainerdAddress != "/tmp/containerd.sock" || cfg.ContainerdNamespace != "test-ns" {
		t.Fatalf("unexpected flat overlay config: %+v", cfg)
	}
	if len(cfg.AllowedOverlaySnapshotRoots) != 1 || cfg.AllowedOverlaySnapshotRoots[0] != "/tmp/containerd/snapshots" {
		t.Fatalf("unexpected snapshot roots: %+v", cfg.AllowedOverlaySnapshotRoots)
	}
	runtime, err := cfg.DelegateRuntime("custom-wrapper")
	if err != nil {
		t.Fatalf("DelegateRuntime: %v", err)
	}
	if runtime != "/custom/runtime" {
		t.Fatalf("unexpected custom delegate: %q", runtime)
	}
}
