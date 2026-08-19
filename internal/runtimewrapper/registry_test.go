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

	"gitcode.com/openFuyao/rootpv/internal/config"
)

func TestLoadAdapterRegistryResolvesTrustedDelegate(t *testing.T) {
	dir := t.TempDir()
	delegate := filepath.Join(dir, "runc")
	if err := os.WriteFile(delegate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := config.RuntimeAdapter{ID: "runc-v1", Mode: config.RuntimeAdapterModeExplicit, OriginalRuntimeClass: "runc", OriginalHandler: "runc", RootfsRuntimeClass: "persistent-rootfs-runc", RootfsHandler: "persistent-rootfs-runc", RuntimeType: config.RuncV2RuntimeType, DelegateBinary: delegate}
	digest, err := adapter.ClusterDigest()
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(dir, "registry.yaml")
	if err := os.WriteFile(registryPath, []byte("formatVersion: rootpv-runtime-adapters-v1\nadapters:\n  runc-v1:\n    mode: Explicit\n    sourceHandler: runc\n    rootpvRuntimeClass: persistent-rootfs-runc\n    rootpvHandler: persistent-rootfs-runc\n    runtimeType: io.containerd.runc.v2\n    delegateBinary: "+delegate+"\n    clusterAdapterDigest: "+digest+"\n    nodeResolvedDigest: sha256:node\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := LoadAdapterRegistry(registryPath, RegistryOptions{AllowedDelegateDirs: []string{dir}})
	if err != nil {
		t.Fatalf("LoadAdapterRegistry: %v", err)
	}
	resolved, err := registry.Resolve("runc-v1", config.RuntimeAdapterModeExplicit, digest)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	realDelegate, err := filepath.EvalSymlinks(delegate)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.DelegateBinary != realDelegate || resolved.SourceHandler != "runc" {
		t.Fatalf("unexpected entry: %+v", resolved)
	}
	byHandlerID, byHandler, err := registry.ResolveByRootfsHandler("persistent-rootfs-runc")
	if err != nil || byHandlerID != "runc-v1" || byHandler.DelegateBinary != realDelegate {
		t.Fatalf("ResolveByRootfsHandler: id=%q adapter=%+v err=%v", byHandlerID, byHandler, err)
	}
	byWrapperID, byWrapper, err := registry.ResolveByWrapperBinary("/usr/local/bin/rootfs-runtime-wrapper-runc-v1")
	if err != nil || byWrapperID != "runc-v1" || byWrapper.DelegateBinary != realDelegate {
		t.Fatalf("ResolveByWrapperBinary: id=%q adapter=%+v err=%v", byWrapperID, byWrapper, err)
	}
}

func TestLoadAdapterRegistryRejectsDelegateOutsideAllowedDirectory(t *testing.T) {
	delegateDir := t.TempDir()
	delegate := filepath.Join(delegateDir, "runc")
	if err := os.WriteFile(delegate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(registryPath, []byte("formatVersion: rootpv-runtime-adapters-v1\nadapters:\n  runc-v1:\n    mode: Explicit\n    sourceHandler: runc\n    rootpvRuntimeClass: persistent-rootfs-runc\n    rootpvHandler: persistent-rootfs-runc\n    runtimeType: io.containerd.runc.v2\n    delegateBinary: "+delegate+"\n    clusterAdapterDigest: sha256:cluster\n    nodeResolvedDigest: sha256:node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAdapterRegistry(registryPath, RegistryOptions{AllowedDelegateDirs: []string{t.TempDir()}}); err == nil {
		t.Fatal("expected delegate allowlist rejection")
	}
}

func TestLoadAdapterRegistryAcceptsGeneratedV2Registry(t *testing.T) {
	delegateDir := t.TempDir()
	delegate := filepath.Join(delegateDir, "vendor-runtime")
	if err := os.WriteFile(delegate, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(registryPath, []byte("formatVersion: rootpv-runtime-adapters-v2\ncatalogGeneration: 12\nadapters:\n  ascend-runc-v2:\n    sourceHandler: ascend\n    rootpvHandler: persistent-rootfs-ascend-runc-v2\n    runtimeType: io.containerd.runc.v2\n    delegateBinary: "+delegate+"\n    clusterDigest: sha256:catalog\n    nodeResolvedDigest: sha256:node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadAdapterRegistry(registryPath, RegistryOptions{AllowedDelegateDirs: []string{delegateDir}})
	if err != nil {
		t.Fatalf("LoadAdapterRegistry: %v", err)
	}
	resolved, err := registry.Resolve("ascend-runc-v2", config.RuntimeAdapterModeExplicit, "sha256:catalog")
	realDelegate, realPathErr := filepath.EvalSymlinks(delegate)
	if err != nil || realPathErr != nil || resolved.DelegateBinary != realDelegate || resolved.RootfsHandler != "persistent-rootfs-ascend-runc-v2" {
		t.Fatalf("unexpected v2 registry resolution: adapter=%#v err=%v", resolved, err)
	}
}
