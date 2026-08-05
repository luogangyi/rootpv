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
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAdmissionConfigRequiresAdapterFileDuringCutover(t *testing.T) {
	_, err := LoadAdmissionConfig("", true)
	if err == nil {
		t.Fatal("expected missing cutover configuration error")
	}
}

func TestLoadAdmissionConfigLoadsRuntimeAdaptersDuringCutover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtimeAdapters.yaml")
	data := []byte(`
defaultRuntimePolicy: Fixed
fixedDefaultAdapter: runc-v1
runtimeAdapters:
  - id: runc-v1
    mode: Explicit
    originalRuntimeClass: runc
    originalHandler: runc
    rootpvRuntimeClass: persistent-rootfs-runc
    rootpvHandler: persistent-rootfs-runc
    runtimeType: io.containerd.runc.v2
    delegateBinary: /usr/bin/runc
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAdmissionConfig(path, true)
	if err != nil {
		t.Fatalf("LoadAdmissionConfig: %v", err)
	}
	if !cfg.RuntimeAdapterCutover {
		t.Fatal("expected cutover mode")
	}
	adapter, err := cfg.RuntimeAdapters.Resolve("runc")
	if err != nil || adapter.ID != "runc-v1" {
		t.Fatalf("unexpected adapter=%+v err=%v", adapter, err)
	}
}

func TestLoadAdmissionConfigKeepsDefaultsWithoutCutover(t *testing.T) {
	cfg, err := LoadAdmissionConfig("", false)
	if err != nil {
		t.Fatalf("LoadAdmissionConfig: %v", err)
	}
	if cfg.RuntimeAdapterCutover || cfg.RuntimeMapping.DefaultRootfsRuntimeClass == "" {
		t.Fatalf("unexpected legacy config: %+v", cfg)
	}
}
