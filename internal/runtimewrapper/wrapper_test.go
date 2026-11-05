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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func TestWrapperCutoverPersistsSandboxDelegateFromRegistry(t *testing.T) {
	registry := writeV1CutoverRegistry(t)
	bundle := writeSpec(t, specs.Spec{Root: &specs.Root{Path: "rootfs"}, Annotations: map[string]string{
		rootfs.AnnotationEffectiveRuntimeClass:      "persistent-rootfs-runc",
		rootfs.AnnotationRuntimeAdapterID:           "runc-v1",
		rootfs.AnnotationRuntimeAdapterMode:         "Explicit",
		rootfs.AnnotationRuntimeAdapterConfigHash:   registry.digest,
		"io.kubernetes.cri.sandbox-runtime-handler": "persistent-rootfs-runc",
		"io.kubernetes.cri.container-type":          "sandbox",
	}})
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	executor := &fakeExecutor{}
	wrapper := Wrapper{Config: Config{RuntimeAdapterCutover: true, RuntimeAdapterRegistry: registry.path, AllowedDelegateDirs: []string{registry.delegateDir}, RuntimeRoot: runtimeRoot}, Executor: executor, SpecStore: FileSpecStore{}}

	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper", []string{"create", "--bundle", bundle, "sandbox-id"}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executor.path == "" || executor.path == "/usr/bin/runc" {
		t.Fatalf("unexpected delegate: %+v", executor)
	}
	state := readState(t, runtimeRoot, "sandbox-id")
	if state.FormatVersion != rootfsstate.FormatDelegationV1 || state.RuntimeAdapterID != "runc-v1" || state.RootfsEnabled {
		t.Fatalf("unexpected delegation state: %+v", state)
	}
	executor.path = ""
	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper", []string{"state", "sandbox-id"}, nil); err != nil {
		t.Fatalf("state Run: %v", err)
	}
	if executor.path == "" {
		t.Fatal("state did not use persisted delegate")
	}
}

func TestWrapperCutoverResolvesSandboxFromGeneratedHandler(t *testing.T) {
	delegateDir := t.TempDir()
	delegate := filepath.Join(delegateDir, "runc")
	if err := os.WriteFile(delegate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(delegateDir, "registry.yaml")
	if err := os.WriteFile(registryPath, []byte("formatVersion: rootpv-runtime-adapters-v2\nadapters:\n  runc-v2:\n    sourceHandler: runc\n    rootpvHandler: persistent-rootfs-runc-v2\n    runtimeType: io.containerd.runc.v2\n    delegateBinary: "+delegate+"\n    clusterDigest: sha256:catalog\n    nodeResolvedDigest: sha256:node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := writeSpec(t, specs.Spec{Root: &specs.Root{Path: "rootfs"}, Annotations: map[string]string{"io.kubernetes.cri.container-type": "sandbox"}})
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	wrapper := Wrapper{Config: Config{RuntimeAdapterCutover: true, RuntimeAdapterRegistry: registryPath, AllowedDelegateDirs: []string{delegateDir}, RuntimeRoot: runtimeRoot}, Executor: &fakeExecutor{}, SpecStore: FileSpecStore{}}
	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc-v2", []string{"create", "--bundle", bundle, "sandbox-id"}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state := readState(t, runtimeRoot, "sandbox-id"); state.RuntimeAdapterID != "runc-v2" || state.RuntimeAdapterMode != string(config.RuntimeAdapterModeExplicit) {
		t.Fatalf("unexpected sandbox delegation state: %+v", state)
	}
}

func TestWrapperCutoverNestedCreatePersistsDelegationBeforeExec(t *testing.T) {
	registry := writeV1CutoverRegistry(t)
	bundle := writeSpec(t, specs.Spec{Root: &specs.Root{Path: "rootfs"}, Annotations: map[string]string{
		rootfs.AnnotationEnabled:                  "true",
		rootfs.AnnotationEffectiveRuntimeClass:    "persistent-rootfs-runc",
		rootfs.AnnotationRuntimeAdapterID:         "runc-v1",
		rootfs.AnnotationRuntimeAdapterMode:       "Explicit",
		rootfs.AnnotationRuntimeAdapterConfigHash: registry.digest,
		"io.kubernetes.cri.sandbox-uid":           "pod-uid-1",
		"io.kubernetes.cri.container-name":        "app",
	}})
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	execErr := errors.New("delegate exec reached")
	wrapper := Wrapper{
		Config: Config{RuntimeAdapterCutover: true, RuntimeAdapterRegistry: registry.path, AllowedDelegateDirs: []string{registry.delegateDir}, RuntimeRoot: runtimeRoot, OverlayMode: OverlayModeNestedRootfs},
		Resolver: fakeResolveFunc(func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
			return apiv1alpha1.ResolveRootfsResponse{Enabled: true, UpperDir: "/pvc/upper", WorkDir: "/pvc/work", MetaDir: "/pvc/meta"}, nil
		}),
		Mounter:   &fakeOverlayMounter{},
		Unmounter: &fakeUnmounter{},
		SpecStore: FileSpecStore{},
		Executor: funcExecutor(func(string, []string, []string) error {
			state := readState(t, runtimeRoot, "container-id")
			if state.FormatVersion != rootfsstate.FormatDelegationV1 || !state.RootfsEnabled || state.RuntimeAdapterID != "runc-v1" || state.DelegateBinary == "" {
				t.Fatalf("delegation state was not persisted before exec: %+v", state)
			}
			if state.UpperDir != "/pvc/upper" || state.WorkDir != "/pvc/work" || state.MetaDir != "/pvc/meta" {
				t.Fatalf("persistent rootfs state was not retained: %+v", state)
			}
			return execErr
		}),
	}

	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper", []string{"create", "--bundle", bundle, "container-id"}, nil)
	if !errors.Is(err, execErr) {
		t.Fatalf("Run error = %v, want %v", err, execErr)
	}
}

func TestWrapperCutoverResolveFailureLeavesNoState(t *testing.T) {
	setup := newFlatCreateRollbackSetup(t, flatCreateSideEffectCase{})
	registry := writeV1CutoverRegistry(t)
	spec := readSpec(t, setup.bundle)
	spec.Annotations[rootfs.AnnotationRuntimeAdapterID] = "runc-v1"
	spec.Annotations[rootfs.AnnotationRuntimeAdapterMode] = "Explicit"
	spec.Annotations[rootfs.AnnotationRuntimeAdapterConfigHash] = registry.digest
	if err := SaveSpec(setup.bundle, &spec); err != nil {
		t.Fatal(err)
	}
	setup.wrapper.Config.RuntimeAdapterCutover = true
	setup.wrapper.Config.RuntimeAdapterRegistry = registry.path
	setup.wrapper.Config.AllowedDelegateDirs = []string{registry.delegateDir}
	resolveErr := errors.New("base_image_mismatch")
	setup.wrapper.Rootfs = &fakeRootfsClient{resolveErr: resolveErr}
	err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper", []string{"create", "--bundle", setup.bundle, "container-id"}, nil)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("expected original resolve failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(setup.runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("failed create left runtime state: %v", err)
	}
}

type v1CutoverRegistry struct {
	delegateDir string
	delegate    string
	digest      string
	path        string
}

func writeV1CutoverRegistry(t *testing.T) v1CutoverRegistry {
	t.Helper()
	delegateDir := t.TempDir()
	delegate := filepath.Join(delegateDir, "runc")
	if err := os.WriteFile(delegate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := config.RuntimeAdapter{ID: "runc-v1", Mode: config.RuntimeAdapterModeExplicit, OriginalRuntimeClass: "runc", OriginalHandler: "runc", RootfsRuntimeClass: "persistent-rootfs-runc", RootfsHandler: "persistent-rootfs-runc", RuntimeType: config.RuncV2RuntimeType, DelegateBinary: delegate}
	digest, err := adapter.ClusterDigest()
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(delegateDir, "registry.yaml")
	data := "formatVersion: rootpv-runtime-adapters-v1\nadapters:\n  runc-v1:\n    mode: Explicit\n    sourceHandler: runc\n    rootpvRuntimeClass: persistent-rootfs-runc\n    rootpvHandler: persistent-rootfs-runc\n    runtimeType: io.containerd.runc.v2\n    delegateBinary: " + delegate + "\n    clusterAdapterDigest: " + digest + "\n    nodeResolvedDigest: sha256:node\n"
	if err := os.WriteFile(registryPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return v1CutoverRegistry{delegateDir: delegateDir, delegate: delegate, digest: digest, path: registryPath}
}

func TestWrapperCutoverDeleteReleasesPersistedLease(t *testing.T) {
	for _, test := range []struct {
		name       string
		unmountErr error
		verifyErr  error
		wantOrder  []string
	}{
		{name: "success", wantOrder: []string{"unmount", "verify", "delegate", "release"}},
		{name: "busy", unmountErr: errors.New("busy"), wantOrder: []string{"unmount"}},
		{name: "still mounted", verifyErr: errors.New("still mounted"), wantOrder: []string{"unmount", "verify"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeRoot := t.TempDir()
			state := mergeDelegationState(flatDeleteState(runtimeRoot), rootfsstate.State{
				RuntimeType: config.RuncV2RuntimeType, DelegateBinary: "/delegate/runc", RootfsEnabled: true,
				RuntimeAdapterID: "runc-v1", RuntimeAdapterMode: "Explicit", RootfsHandler: "persistent-rootfs-runc",
				ClusterAdapterDigest: "sha256:cluster", NodeResolvedDigest: "sha256:node",
			})
			writeState(t, runtimeRoot, state.ContainerID, state)
			var order []string
			client := &orderedDeleteClient{order: &order}
			wrapper := Wrapper{
				Config:          Config{RuntimeAdapterCutover: true, RuntimeRoot: runtimeRoot},
				Rootfs:          client,
				NormalUnmounter: &orderedNormalUnmounter{order: &order, err: test.unmountErr},
				UnmountVerifier: &orderedUnmountVerifier{order: &order, err: test.verifyErr},
				CommandRunner:   &orderedCommandRunner{order: &order},
				Unmounter:       &fakeUnmounter{err: test.unmountErr},
				Executor:        &fakeExecutor{},
			}
			err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper", []string{"delete", "--force", state.ContainerID}, nil)
			wantFailure := test.unmountErr != nil || test.verifyErr != nil
			if (err != nil) != wantFailure {
				t.Fatalf("want failure=%v, got %v; operations=%v", wantFailure, err, order)
			}
			if !reflect.DeepEqual(order, test.wantOrder) {
				t.Fatalf("order=%v want=%v", order, test.wantOrder)
			}
			if !wantFailure && (client.releaseRequest.LeaseID != state.LeaseID || client.releaseRequest.ContainerID != state.ContainerID) {
				t.Fatalf("released wrong lease: %+v", client.releaseRequest)
			}
			_, stateErr := rootfsstate.Load(runtimeRoot, state.ContainerID)
			if wantFailure && stateErr != nil {
				t.Fatalf("recovery state lost: %v", stateErr)
			}
			if !wantFailure && !os.IsNotExist(stateErr) {
				t.Fatalf("runtime state should be removed: %v", stateErr)
			}
		})
	}
}

func TestWrapperCreateMountsRewritesSpecAndDelegates(t *testing.T) {
	bundle := writeSpec(t, specs.Spec{
		Root: &specs.Root{Path: "rootfs", Readonly: true},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:               "true",
			rootfs.AnnotationEffectiveRuntimeClass: "persistent-rootfs-nvidia",
			"io.kubernetes.cri.sandbox-uid":        "pod-uid-1",
			"io.kubernetes.cri.container-name":     "dev",
		},
	})
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	executor := &fakeExecutor{}
	mounter := &fakeOverlayMounter{}
	logger := &fakeLogger{}
	wrapper := Wrapper{
		Config: Config{
			DefaultDelegateRuntime: "/usr/bin/runc",
			RuntimeRoot:            runtimeRoot,
		},
		Resolver: fakeResolveFunc(func(_ context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
			if req.ContainerID != "container-id" || req.ContainerName != "dev" {
				t.Fatalf("unexpected resolve request: %+v", req)
			}
			return apiv1alpha1.ResolveRootfsResponse{Enabled: true, UpperDir: "/pvc/upper", WorkDir: "/pvc/work"}, nil
		}),
		Mounter:   mounter,
		Unmounter: &fakeUnmounter{},
		Executor:  executor,
		Logger:    logger,
	}

	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", bundle, "container-id"}, []string{"A=B"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantMerged := filepath.Join(runtimeRoot, "container-id", "merged")
	if mounter.call != (overlayMountCall{lower: filepath.Join(bundle, "rootfs"), upper: "/pvc/upper", work: "/pvc/work", merged: wantMerged}) {
		t.Fatalf("unexpected mount call: %+v", mounter.call)
	}
	spec := readSpec(t, bundle)
	if spec.Root.Path != wantMerged || spec.Root.Readonly {
		t.Fatalf("spec root was not rewritten: %+v", spec.Root)
	}
	if executor.path != "/usr/bin/runc" || !reflect.DeepEqual(executor.args, []string{"/usr/bin/runc", "create", "--bundle", bundle, "container-id"}) {
		t.Fatalf("unexpected exec: path=%q args=%v", executor.path, executor.args)
	}
	state := readState(t, runtimeRoot, "container-id")
	if state.ContainerID != "container-id" || state.PodUID != "pod-uid-1" || state.ContainerName != "dev" {
		t.Fatalf("unexpected wrapper state: %+v", state)
	}
	if !logger.contains("persistent_rootfs_mount container_id=container-id") ||
		!logger.contains("runtime_class=persistent-rootfs-nvidia") ||
		!logger.contains("delegate_runtime=/usr/bin/runc") ||
		!logger.contains("mount_result=success") ||
		!logger.contains("pod_uid=pod-uid-1") ||
		!logger.contains("upper=/pvc/upper") ||
		!logger.contains("merged="+wantMerged) {
		t.Fatalf("expected mount context in logs, got %#v", logger.entries)
	}
}

func TestWrapperDelegatesSandboxWithoutMounting(t *testing.T) {
	bundle := writeSpec(t, specs.Spec{
		Root: &specs.Root{Path: "rootfs"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:           "true",
			"io.kubernetes.cri.container-type": "sandbox",
		},
	})
	executor := &fakeExecutor{}
	mounter := &fakeOverlayMounter{}
	wrapper := testWrapper(executor, mounter)

	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", bundle, "sandbox-id"}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mounter.called {
		t.Fatalf("sandbox should not mount")
	}
	if executor.path != "/usr/bin/runc" {
		t.Fatalf("sandbox did not delegate: %+v", executor)
	}
}

func TestWrapperFailsClosedBeforeDelegateWhenResolveFails(t *testing.T) {
	bundle := writeSpec(t, specs.Spec{
		Root: &specs.Root{Path: "rootfs"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:           "true",
			"io.kubernetes.cri.container-name": "dev",
		},
	})
	executor := &fakeExecutor{}
	wrapper := testWrapper(executor, &fakeOverlayMounter{})
	wrapper.Rootfs = fakeRootfsClientFunc(func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
		return apiv1alpha1.ResolveRootfsResponse{}, errors.New("rootfsd unavailable")
	})

	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", bundle, "container-id"}, nil)
	if err == nil {
		t.Fatalf("expected fail-closed error")
	}
	if executor.called {
		t.Fatalf("delegate should not be executed after resolve failure")
	}
}

func TestWrapperRetriesTransientResolveError(t *testing.T) {
	bundle := writeSpec(t, specs.Spec{
		Root: &specs.Root{Path: "rootfs"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:           "true",
			"io.kubernetes.cri.sandbox-uid":    "pod-uid-1",
			"io.kubernetes.cri.container-name": "dev",
		},
	})
	executor := &fakeExecutor{}
	attempts := 0
	wrapper := testWrapper(executor, &fakeOverlayMounter{})
	wrapper.ResolveRetryTimeout = time.Second
	wrapper.ResolveRetryInterval = time.Millisecond
	wrapper.Rootfs = fakeRootfsClientFunc(func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
		attempts++
		if attempts < 3 {
			return apiv1alpha1.ResolveRootfsResponse{}, fmt.Errorf(`rootfsd request /rootfs/resolve failed: 409 Conflict: persistent rootfs pod "pod-uid-1" is not registered`)
		}
		return apiv1alpha1.ResolveRootfsResponse{Enabled: true, UpperDir: "/pvc/upper", WorkDir: "/pvc/work"}, nil
	})

	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", bundle, "container-id"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 resolve attempts, got %d", attempts)
	}
	if !executor.called {
		t.Fatalf("delegate should be executed after transient resolve succeeds")
	}
}

func TestWrapperCleansMountedOverlayWhenSpecSaveFails(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	merged := filepath.Join(runtimeRoot, "container-id", "merged")
	executor := &fakeExecutor{}
	mounter := &fakeOverlayMounter{}
	unmounter := &fakeUnmounter{}
	wrapper := Wrapper{
		Config: Config{
			DefaultDelegateRuntime: "/usr/bin/runc",
			RuntimeRoot:            runtimeRoot,
		},
		Resolver: fakeResolveFunc(func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
			return apiv1alpha1.ResolveRootfsResponse{Enabled: true, UpperDir: "/pvc/upper", WorkDir: "/pvc/work"}, nil
		}),
		Mounter:   mounter,
		Unmounter: unmounter,
		Executor:  executor,
		SpecStore: &fakeSpecStore{
			spec: &specs.Spec{
				Root: &specs.Root{Path: "rootfs"},
				Annotations: map[string]string{
					rootfs.AnnotationEnabled:           "true",
					"io.kubernetes.cri.container-name": "dev",
				},
			},
			saveErr: errors.New("save failed"),
		},
	}

	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", t.TempDir(), "container-id"}, nil)
	if err == nil {
		t.Fatalf("expected save failure")
	}
	if !mounter.called {
		t.Fatalf("expected overlay mount before save failure")
	}
	if unmounter.path != merged {
		t.Fatalf("expected cleanup unmount %q, got %q", merged, unmounter.path)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeRoot, "container-id")); !os.IsNotExist(statErr) {
		t.Fatalf("runtime dir should be removed after save failure, stat err=%v", statErr)
	}
	if executor.called {
		t.Fatalf("delegate should not execute after save failure")
	}
}

func TestWrapperDeleteUnmountsAndDelegates(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	containerDir := filepath.Join(runtimeRoot, "container-id")
	merged := filepath.Join(containerDir, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	writeState(t, runtimeRoot, "container-id", rootfsstate.State{
		ContainerID:   "container-id",
		PodUID:        "pod-uid-1",
		ContainerName: "dev",
		MetaDir:       "/pvc/meta",
	})
	executor := &fakeExecutor{}
	unmounter := &fakeUnmounter{}
	releaser := &fakeRootfsClient{}
	wrapper := Wrapper{
		Config: Config{
			DefaultDelegateRuntime: "/usr/bin/runc",
			RuntimeRoot:            runtimeRoot,
		},
		Rootfs:    releaser,
		Mounter:   &fakeOverlayMounter{},
		Unmounter: unmounter,
		Executor:  executor,
	}

	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"delete", "container-id"}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if unmounter.path != merged {
		t.Fatalf("unexpected unmount path: %q", unmounter.path)
	}
	if _, err := os.Stat(containerDir); !os.IsNotExist(err) {
		t.Fatalf("container runtime dir should be removed, stat err=%v", err)
	}
	if executor.path != "/usr/bin/runc" {
		t.Fatalf("delete did not delegate: %+v", executor)
	}
	if releaser.releaseRequest.ContainerID != "container-id" || releaser.releaseRequest.MetaDir != "/pvc/meta" {
		t.Fatalf("unexpected release request: %+v", releaser.releaseRequest)
	}
}

func TestWrapperDeleteDelegatesWhenReleaseFails(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	containerDir := filepath.Join(runtimeRoot, "container-id")
	if err := os.MkdirAll(filepath.Join(containerDir, "merged"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeState(t, runtimeRoot, "container-id", rootfsstate.State{ContainerID: "container-id", MetaDir: "/pvc/meta"})
	executor := &fakeExecutor{}
	wrapper := Wrapper{
		Config: Config{
			DefaultDelegateRuntime: "/usr/bin/runc",
			RuntimeRoot:            runtimeRoot,
		},
		Rootfs:    &fakeRootfsClient{releaseErr: errors.New("rootfsd down")},
		Mounter:   &fakeOverlayMounter{},
		Unmounter: &fakeUnmounter{},
		Executor:  executor,
	}

	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"delete", "container-id"}, nil); err != nil {
		t.Fatalf("delete should delegate despite release failure: %v", err)
	}
	if executor.path != "/usr/bin/runc" {
		t.Fatalf("delete did not delegate: %+v", executor)
	}
}

func TestWrapperDeleteDelegatesWhenUnmountFails(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	containerDir := filepath.Join(runtimeRoot, "container-id")
	merged := filepath.Join(containerDir, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	writeState(t, runtimeRoot, "container-id", rootfsstate.State{ContainerID: "container-id", MetaDir: "/pvc/meta"})
	executor := &fakeExecutor{}
	unmounter := &fakeUnmounter{err: errors.New("device busy")}
	logger := &fakeLogger{}
	wrapper := Wrapper{
		Config: Config{
			DefaultDelegateRuntime: "/usr/bin/runc",
			RuntimeRoot:            runtimeRoot,
		},
		Rootfs:    &fakeRootfsClient{},
		Mounter:   &fakeOverlayMounter{},
		Unmounter: unmounter,
		Executor:  executor,
		Logger:    logger,
	}

	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"delete", "container-id"}, nil); err != nil {
		t.Fatalf("delete should delegate despite unmount failure: %v", err)
	}
	if unmounter.path != merged {
		t.Fatalf("unexpected unmount path: %q", unmounter.path)
	}
	if executor.path != "/usr/bin/runc" {
		t.Fatalf("delete did not delegate: %+v", executor)
	}
	if !logger.contains("persistent_rootfs_unmount_failed container_id=container-id") {
		t.Fatalf("expected unmount failure log, got %#v", logger.entries)
	}
}

func testWrapper(executor *fakeExecutor, mounter *fakeOverlayMounter) Wrapper {
	return Wrapper{
		Config: Config{
			DefaultDelegateRuntime: "/usr/bin/runc",
			RuntimeRoot:            filepath.Join(os.TempDir(), "rootpv-test-runtime"),
		},
		Rootfs: fakeRootfsClientFunc(func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
			return apiv1alpha1.ResolveRootfsResponse{Enabled: true, UpperDir: "/pvc/upper", WorkDir: "/pvc/work"}, nil
		}),
		Mounter:   mounter,
		Unmounter: &fakeUnmounter{},
		Executor:  executor,
	}
}

func writeSpec(t *testing.T, spec specs.Spec) string {
	t.Helper()
	bundle := t.TempDir()
	if spec.Root == nil {
		spec.Root = &specs.Root{Path: "rootfs"}
	}
	rootPath := spec.Root.Path
	if !filepath.IsAbs(rootPath) {
		rootPath = filepath.Join(bundle, rootPath)
	}
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func readSpec(t *testing.T, bundle string) specs.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec specs.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

type fakeResolveFunc func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error)

func (f fakeResolveFunc) ResolveRootfs(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	if f == nil {
		return apiv1alpha1.ResolveRootfsResponse{}, nil
	}
	return f(ctx, req)
}

type fakeRootfsClient struct {
	resolveResponse apiv1alpha1.ResolveRootfsResponse
	resolveErr      error
	releaseRequest  apiv1alpha1.ReleaseRootfsRequest
	releaseErr      error
}

func (c *fakeRootfsClient) ResolveRootfs(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	return c.resolveResponse, c.resolveErr
}

func (c *fakeRootfsClient) ReleaseRootfs(ctx context.Context, req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	c.releaseRequest = req
	return apiv1alpha1.ReleaseRootfsResponse{Released: true}, c.releaseErr
}

func (c *fakeRootfsClient) MarkMounted(context.Context, apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	return apiv1alpha1.MarkMountedResponse{Mounted: true}, nil
}

func (c *fakeRootfsClient) BeginCreate(context.Context, apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	return apiv1alpha1.BeginCreateResponse{AttemptToken: "attempt-token"}, nil
}

func (c *fakeRootfsClient) RegisterDelegate(context.Context, apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	return apiv1alpha1.RegisterDelegateResponse{Registered: true}, nil
}

func (c *fakeRootfsClient) CommitCreate(context.Context, apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	return apiv1alpha1.CommitCreateResponse{Created: true}, nil
}

type fakeRootfsClientFunc func(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error)

func (f fakeRootfsClientFunc) ResolveRootfs(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	return f(ctx, req)
}

func (f fakeRootfsClientFunc) ReleaseRootfs(ctx context.Context, req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	return apiv1alpha1.ReleaseRootfsResponse{Released: true}, nil
}

func (f fakeRootfsClientFunc) MarkMounted(context.Context, apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	return apiv1alpha1.MarkMountedResponse{Mounted: true}, nil
}

func (f fakeRootfsClientFunc) BeginCreate(context.Context, apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	return apiv1alpha1.BeginCreateResponse{AttemptToken: "attempt-token"}, nil
}

func (f fakeRootfsClientFunc) RegisterDelegate(context.Context, apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	return apiv1alpha1.RegisterDelegateResponse{Registered: true}, nil
}

func (f fakeRootfsClientFunc) CommitCreate(context.Context, apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	return apiv1alpha1.CommitCreateResponse{Created: true}, nil
}

type overlayMountCall struct {
	lower  string
	upper  string
	work   string
	merged string
}

type fakeOverlayMounter struct {
	called bool
	call   overlayMountCall
}

func (m *fakeOverlayMounter) MountOverlay(lower, upper, work, merged string, _ []string) error {
	m.called = true
	m.call = overlayMountCall{lower: lower, upper: upper, work: work, merged: merged}
	return nil
}

type fakeUnmounter struct {
	path string
	err  error
}

func (u *fakeUnmounter) Unmount(path string) error {
	u.path = path
	return u.err
}

type fakeExecutor struct {
	called bool
	path   string
	args   []string
	env    []string
}

type funcExecutor func(string, []string, []string) error

func (f funcExecutor) Exec(path string, args []string, env []string) error {
	return f(path, args, env)
}

func (e *fakeExecutor) Exec(path string, args []string, env []string) error {
	e.called = true
	e.path = path
	e.args = append([]string(nil), args...)
	e.env = append([]string(nil), env...)
	return nil
}

type fakeLogger struct {
	entries []string
}

func (l *fakeLogger) Print(v ...any) {
	l.entries = append(l.entries, fmt.Sprint(v...))
}

func (l *fakeLogger) contains(needle string) bool {
	for _, entry := range l.entries {
		if strings.Contains(entry, needle) {
			return true
		}
	}
	return false
}

type fakeSpecStore struct {
	spec    *specs.Spec
	saveErr error
}

func writeState(t *testing.T, runtimeRoot, containerID string, state rootfsstate.State) {
	t.Helper()
	if err := rootfsstate.Save(runtimeRoot, containerID, state); err != nil {
		t.Fatalf("rootfsstate.Save: %v", err)
	}
}

func readState(t *testing.T, runtimeRoot, containerID string) rootfsstate.State {
	t.Helper()
	state, err := rootfsstate.Load(runtimeRoot, containerID)
	if err != nil {
		t.Fatalf("rootfsstate.Load: %v", err)
	}
	return state
}

func (s *fakeSpecStore) Load(bundle string) (*specs.Spec, error) {
	return s.spec, nil
}

func (s *fakeSpecStore) Save(bundle string, spec *specs.Spec) error {
	return s.saveErr
}
