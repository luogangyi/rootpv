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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadControllerConfigUsesAdapterFileInCutoverMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtimeAdapters.yaml")
	if err := os.WriteFile(path, []byte(`
defaultRuntimePolicy: NodeDefault
runtimeAdapters: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadControllerConfig(options{runtimeAdapterCutover: true, runtimeAdaptersConfig: path})
	if err != nil {
		t.Fatalf("loadControllerConfig: %v", err)
	}
	if !cfg.RuntimeAdapterCutover {
		t.Fatal("expected cutover config")
	}
}

func TestLoadControllerConfigRejectsMissingAdapterFileInCutoverMode(t *testing.T) {
	if _, err := loadControllerConfig(options{runtimeAdapterCutover: true}); err == nil {
		t.Fatal("expected missing adapter config error")
	}
}
