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
	"reflect"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

func TestSaveAndLoadSpecRoundTrip(t *testing.T) {
	bundle := t.TempDir()
	want := &specs.Spec{
		Version: "1.0.2",
		Root:    &specs.Root{Path: "rootfs", Readonly: true},
		Process: &specs.Process{Args: []string{"/bin/sh"}, Cwd: "/"},
		Annotations: map[string]string{
			"io.kubernetes.cri.container-name": "app",
		},
	}

	if err := SaveSpec(bundle, want); err != nil {
		t.Fatalf("SaveSpec: %v", err)
	}
	got, err := LoadSpec(bundle)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected round-trip spec:\n got: %+v\nwant: %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(bundle, "config.json.rootpv.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary spec file should be removed, stat error: %v", err)
	}
}

func TestRewriteRootMakesRootfsWritable(t *testing.T) {
	spec := &specs.Spec{Root: &specs.Root{Path: "old-rootfs", Readonly: true}}
	if err := RewriteRoot(spec, "/var/lib/rootpv/merged"); err != nil {
		t.Fatalf("RewriteRoot: %v", err)
	}
	if spec.Root.Path != "/var/lib/rootpv/merged" || spec.Root.Readonly {
		t.Fatalf("root was not rewritten as writable: %+v", spec.Root)
	}
}

func TestOriginalRootPathRequiresBundleForRelativePath(t *testing.T) {
	_, err := OriginalRootPath(&specs.Spec{Root: &specs.Root{Path: "rootfs"}}, "")
	if err == nil {
		t.Fatal("expected relative root path without bundle to fail")
	}
}

func TestRootfsTargetContainersRejectsInvalidAdvancedSpec(t *testing.T) {
	targets, configured := rootfsTargetContainers(map[string]string{
		rootfs.AnnotationSpec: "not-json",
	})
	if !configured {
		t.Fatal("invalid advanced configuration must remain configured and fail closed")
	}
	if len(targets) != 0 {
		t.Fatalf("invalid advanced configuration must not select containers: %+v", targets)
	}
}

func TestSpecClassifiesSandboxAsDelegateOnly(t *testing.T) {
	spec := &specs.Spec{Annotations: map[string]string{
		"io.kubernetes.cri.container-type": "sandbox",
		rootfs.AnnotationEnabled:           "true",
	}}
	if !IsSandbox(spec) {
		t.Fatalf("expected sandbox classification")
	}
	if ShouldMountPersistentRootfs(spec) {
		t.Fatalf("sandbox must not mount persistent rootfs")
	}
}

func TestSpecWithoutPersistentAnnotationDelegates(t *testing.T) {
	spec := &specs.Spec{Annotations: map[string]string{
		"io.kubernetes.cri.container-name": "dev",
	}}
	if ShouldMountPersistentRootfs(spec) {
		t.Fatalf("spec without persistent annotation should delegate")
	}
}

func TestSpecWithWrapperMarkerMountsPersistentRootfs(t *testing.T) {
	spec := &specs.Spec{Annotations: map[string]string{
		rootfs.AnnotationEnabled:           "wrapper",
		rootfs.AnnotationWrapperEnabled:    "true",
		"io.kubernetes.cri.container-name": "dev",
	}}
	if !ShouldMountPersistentRootfs(spec) {
		t.Fatal("wrapper marker should mount persistent rootfs")
	}
}

func TestShouldMountPersistentRootfsOnlyForConfiguredContainer(t *testing.T) {
	annotations := map[string]string{
		rootfs.AnnotationEnabled:   "true",
		rootfs.AnnotationContainer: "rootfs-app",
	}
	rootfsSpec := &specs.Spec{Annotations: map[string]string{
		rootfs.AnnotationEnabled:           annotations[rootfs.AnnotationEnabled],
		rootfs.AnnotationContainer:         annotations[rootfs.AnnotationContainer],
		"io.kubernetes.cri.container-name": "rootfs-app",
	}}
	ordinarySpec := &specs.Spec{Annotations: map[string]string{
		rootfs.AnnotationEnabled:           annotations[rootfs.AnnotationEnabled],
		rootfs.AnnotationContainer:         annotations[rootfs.AnnotationContainer],
		"io.kubernetes.cri.container-name": "ordinary",
	}}
	if !ShouldMountPersistentRootfs(rootfsSpec) {
		t.Fatal("configured RootPV container should mount persistent rootfs")
	}
	if ShouldMountPersistentRootfs(ordinarySpec) {
		t.Fatal("ordinary container should delegate without mounting persistent rootfs")
	}
}

func TestShouldMountPersistentRootfsOnlyForConfiguredAdvancedTargets(t *testing.T) {
	annotations := map[string]string{
		rootfs.AnnotationEnabled: "true",
		rootfs.AnnotationSpec:    `[{"containerName":"first"},{"containerName":"second"}]`,
	}
	firstSpec := &specs.Spec{Annotations: map[string]string{
		rootfs.AnnotationEnabled:           annotations[rootfs.AnnotationEnabled],
		rootfs.AnnotationSpec:              annotations[rootfs.AnnotationSpec],
		"io.kubernetes.cri.container-name": "first",
	}}
	ordinarySpec := &specs.Spec{Annotations: map[string]string{
		rootfs.AnnotationEnabled:           annotations[rootfs.AnnotationEnabled],
		rootfs.AnnotationSpec:              annotations[rootfs.AnnotationSpec],
		"io.kubernetes.cri.container-name": "ordinary",
	}}
	if !ShouldMountPersistentRootfs(firstSpec) {
		t.Fatal("advanced RootPV target should mount persistent rootfs")
	}
	if ShouldMountPersistentRootfs(ordinarySpec) {
		t.Fatal("ordinary container should not match advanced RootPV targets")
	}
}

func TestBuildResolveRequestFromOCIAnnotations(t *testing.T) {
	bundle := t.TempDir()
	spec := &specs.Spec{
		Root: &specs.Root{Path: "rootfs"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:               "true",
			rootfs.AnnotationEffectiveRuntimeClass: "persistent-rootfs-nvidia",
			"io.kubernetes.cri.sandbox-uid":        "pod-uid-1",
			"io.kubernetes.cri.sandbox-name":       "gpu-dev",
			"io.kubernetes.pod.namespace":          "ai-dev",
			"io.kubernetes.cri.container-name":     "dev",
			"io.kubernetes.cri.sandbox-id":         "sandbox-id",
			"io.kubernetes.cri.image-name":         "registry/rootpv:test",
			"containerd.io/snapshot/cri.image-ref": "sha256:image",
		},
	}
	req, err := BuildResolveRequest(spec, Invocation{ContainerID: "container-id", Bundle: bundle})
	if err != nil {
		t.Fatalf("BuildResolveRequest: %v", err)
	}

	wantRoot := filepath.Join(bundle, "rootfs")
	want := apiv1alpha1.ResolveRootfsRequest{
		ContainerID:      "container-id",
		Bundle:           bundle,
		OriginalRootPath: wantRoot,
		RuntimeClass:     "persistent-rootfs-nvidia",
		PodUID:           "pod-uid-1",
		PodName:          "gpu-dev",
		Namespace:        "ai-dev",
		ContainerName:    "dev",
		SandboxID:        "sandbox-id",
		Image:            "registry/rootpv:test",
		ImageID:          "sha256:image",
	}
	want.Annotations = req.Annotations
	want.Labels = req.Labels
	if !reflect.DeepEqual(req, want) {
		t.Fatalf("unexpected request:\n got: %+v\nwant: %+v", req, want)
	}
	if req.Labels[rootfs.AnnotationEnabled] != "true" || req.Annotations[rootfs.AnnotationEnabled] != "true" {
		t.Fatalf("annotations were not preserved in request: %+v", req)
	}
}

func TestBuildResolveRequestUsesNamespaceNameWhenUIDMissing(t *testing.T) {
	bundle := t.TempDir()
	spec := &specs.Spec{
		Root: &specs.Root{Path: "/var/lib/containerd/bundle/rootfs"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:       "true",
			"io.kubernetes.pod.name":       "gpu-dev",
			"io.kubernetes.pod.namespace":  "ai-dev",
			"io.kubernetes.container.name": "dev",
		},
	}
	req, err := BuildResolveRequest(spec, Invocation{ContainerID: "container-id", Bundle: bundle})
	if err != nil {
		t.Fatalf("BuildResolveRequest: %v", err)
	}
	if req.PodUID != "" || req.PodName != "gpu-dev" || req.Namespace != "ai-dev" || req.ContainerName != "dev" {
		t.Fatalf("unexpected identity fallback request: %+v", req)
	}
	if req.OriginalRootPath != "/var/lib/containerd/bundle/rootfs" {
		t.Fatalf("absolute root path should be preserved, got %q", req.OriginalRootPath)
	}
}
