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

package rootfsd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

func TestPreparePodCreatesAnchorRootfsDirsAndLock(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)

	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod: %v", err)
	}

	for _, path := range []string{
		filepath.Join(tmp, ".rootpv-anchor", "gpu-dev", "dev"),
		filepath.Join(tmp, "containers", "dev", "upper"),
		filepath.Join(tmp, "containers", "dev", "work"),
		filepath.Join(tmp, "containers", "dev", "meta"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be directory", path)
		}
	}

	lockPath := filepath.Join(tmp, "containers", "dev", "meta", "lock.json")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var lock apiv1alpha1.LockInfo
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("unmarshal lock: %v", err)
	}
	if lock.PodUID != pod.PodUID || lock.ContainerName != "dev" || lock.NodeName != "gpu-node-1" {
		t.Fatalf("unexpected lock: %+v", lock)
	}

	cleanPath := filepath.Join(tmp, "containers", "dev", "meta", "clean-shutdown.json")
	data, err = os.ReadFile(cleanPath)
	if err != nil {
		t.Fatalf("read clean-shutdown: %v", err)
	}
	var clean apiv1alpha1.CleanShutdownInfo
	if err := json.Unmarshal(data, &clean); err != nil {
		t.Fatalf("unmarshal clean-shutdown: %v", err)
	}
	if clean.CleanShutdown {
		t.Fatalf("PreparePod should mark rootfs as not cleanly released: %+v", clean)
	}
}

func TestRootfsPathLockIsRemovedAfterOperation(t *testing.T) {
	manager := NewManager(ManagerOptions{})
	rootPath := filepath.Join(t.TempDir(), "persistent")
	if err := manager.withRootfsPathLock(rootPath, func() error { return nil }); err != nil {
		t.Fatalf("withRootfsPathLock: %v", err)
	}
	manager.prepareLocksMu.Lock()
	defer manager.prepareLocksMu.Unlock()
	if len(manager.prepareLocks) != 0 {
		t.Fatalf("unused path lock remains in memory: %+v", manager.prepareLocks)
	}
}

func TestPreparePodRefusesUnmountedPVCHostPath(t *testing.T) {
	pvcHostPath := filepath.Join(t.TempDir(), "pods", "pod-uid-1", "volumes", "kubernetes.io~csi", "rootfs-pvc", "mount")
	if err := os.MkdirAll(pvcHostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1", MountInfoPath: emptyMountInfo(t)})
	pod := testPodRootfs(pvcHostPath)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	err := manager.PreparePod(context.Background(), pod.PodUID)
	if err == nil || !strings.Contains(err.Error(), "PVC host path is not mounted") {
		t.Fatalf("PreparePod error=%v, want unmounted PVC host path", err)
	}
	if _, err := os.Stat(filepath.Join(pvcHostPath, ".rootpv-anchor")); !os.IsNotExist(err) {
		t.Fatalf("PreparePod created anchor below an unmounted path, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(pvcHostPath, "containers")); !os.IsNotExist(err) {
		t.Fatalf("PreparePod created rootfs state below an unmounted path, stat err=%v", err)
	}
}

func TestPreparePodAllowsMountedPVCHostPath(t *testing.T) {
	pvcHostPath := filepath.Join(t.TempDir(), "pods", "pod-uid-1", "volumes", "kubernetes.io~csi", "rootfs-pvc", "mount")
	if err := os.MkdirAll(pvcHostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1", MountInfoPath: mountInfoForPath(t, pvcHostPath)})
	pod := testPodRootfs(pvcHostPath)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pvcHostPath, "containers", "dev", "meta", "lock.json")); err != nil {
		t.Fatalf("PreparePod did not write rootfs metadata below the mounted PVC path: %v", err)
	}
}

func TestFlatResolveRefusesUnmountedPVCHostPath(t *testing.T) {
	pvcHostPath := filepath.Join(t.TempDir(), "pods", "pod-uid-1", "volumes", "kubernetes.io~csi", "rootfs-pvc", "mount")
	if err := os.MkdirAll(pvcHostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1", MountInfoPath: emptyMountInfo(t)})
	pod := testPodRootfs(pvcHostPath)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	req := resolveRequest(pod, "container-id")
	req.Snapshotter = "overlayfs"
	req.RootfsChainID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	_, err := manager.ResolveRootfs(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "PVC host path is not mounted") {
		t.Fatalf("ResolveRootfs error=%v, want unmounted PVC host path", err)
	}
	if _, err := os.Stat(filepath.Join(pvcHostPath, ".rootpv-anchor")); !os.IsNotExist(err) {
		t.Fatalf("ResolveRootfs created anchor below an unmounted path, stat err=%v", err)
	}
}

func TestReleaseRootfsRefusesUnmountedPVCHostPath(t *testing.T) {
	pvcHostPath := filepath.Join(t.TempDir(), "pods", "pod-uid-1", "volumes", "kubernetes.io~csi", "rootfs-pvc", "mount")
	if err := os.MkdirAll(pvcHostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1", MountInfoPath: emptyMountInfo(t)})
	pod := testPodRootfs(pvcHostPath)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	_, err := manager.ReleaseRootfs(context.Background(), apiv1alpha1.ReleaseRootfsRequest{
		PodUID:        pod.PodUID,
		ContainerName: "dev",
		ContainerID:   "container-id",
	})
	if err == nil || !strings.Contains(err.Error(), "PVC host path is not mounted") {
		t.Fatalf("ReleaseRootfs error=%v, want unmounted PVC host path", err)
	}
	if _, err := os.Stat(filepath.Join(pvcHostPath, "containers")); !os.IsNotExist(err) {
		t.Fatalf("ReleaseRootfs wrote rootfs state below an unmounted path, stat err=%v", err)
	}
}

func TestManagerFlatResolveHoldsLeaseUntilRelease(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	req := resolveRequest(pod, "container-id")
	req.Snapshotter = "overlayfs"
	req.RootfsChainID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resp, err := manager.ResolveRootfs(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if resp.LeaseID == "" {
		t.Fatal("flat resolve did not return a lease ID")
	}
	rootPath := filepath.Join(tmp, "containers", "dev")
	if resp.UpperDir != filepath.Join(rootPath, "upper") || resp.WorkDir != filepath.Join(rootPath, "work") || resp.MetaDir != filepath.Join(rootPath, "meta") {
		t.Fatalf("unexpected rootfs paths: %+v", resp)
	}

	second := req
	second.ContainerID = "container-id-2"
	if _, err := manager.ResolveRootfs(context.Background(), second); err == nil || !strings.Contains(err.Error(), "persistent_upper_in_use") {
		t.Fatalf("expected held lease conflict, got %v", err)
	}

	released, err := manager.ReleaseRootfs(context.Background(), apiv1alpha1.ReleaseRootfsRequest{LeaseID: resp.LeaseID})
	if err != nil {
		t.Fatalf("ReleaseRootfs: %v", err)
	}
	if !released.Released {
		t.Fatalf("lease was not released: %+v", released)
	}
	if _, err := manager.ReleaseRootfs(context.Background(), apiv1alpha1.ReleaseRootfsRequest{LeaseID: resp.LeaseID}); err != nil {
		t.Fatalf("idempotent ReleaseRootfs: %v", err)
	}
}

func TestManagerFlatLifecycleAPI(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	req := resolveRequest(pod, "container-id")
	req.Snapshotter = "overlayfs"
	req.RootfsChainID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	resolved, err := manager.ResolveRootfs(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}

	mounted, err := manager.MarkMounted(context.Background(), apiv1alpha1.MarkMountedRequest{
		LeaseID:          resolved.LeaseID,
		RuntimeStatePath: "/run/rootpv/rootfs/container-id.json",
	})
	if err != nil || !mounted.Mounted {
		t.Fatalf("MarkMounted: response=%+v err=%v", mounted, err)
	}
	begin, err := manager.BeginCreate(context.Background(), apiv1alpha1.BeginCreateRequest{
		LeaseID:          resolved.LeaseID,
		ContainerID:      "container-id",
		WrapperPID:       101,
		WrapperStartTime: 202,
	})
	if err != nil || begin.AttemptToken == "" {
		t.Fatalf("BeginCreate: response=%+v err=%v", begin, err)
	}
	registered, err := manager.RegisterDelegate(context.Background(), apiv1alpha1.RegisterDelegateRequest{
		LeaseID:           resolved.LeaseID,
		AttemptToken:      begin.AttemptToken,
		DelegatePID:       303,
		DelegateStartTime: 404,
	})
	if err != nil || !registered.Registered {
		t.Fatalf("RegisterDelegate: response=%+v err=%v", registered, err)
	}
	created, err := manager.CommitCreate(context.Background(), apiv1alpha1.CommitCreateRequest{
		LeaseID:      resolved.LeaseID,
		AttemptToken: begin.AttemptToken,
	})
	if err != nil || !created.Created {
		t.Fatalf("CommitCreate: response=%+v err=%v", created, err)
	}
}

func TestManagerFlatResolveRequiresCompleteBaseIdentity(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	req := resolveRequest(pod, "container-id")
	req.Snapshotter = "overlayfs"
	if _, err := manager.ResolveRootfs(context.Background(), req); err == nil || !strings.Contains(err.Error(), "snapshotter and rootfs chain ID") {
		t.Fatalf("expected incomplete base identity error, got %v", err)
	}
}

func TestRegisterPodHonorsCanceledContext(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.RegisterPod(ctx, testPodRootfs(tmp))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestResolveRootfsCleansWorkdirWithoutDeletingUpperdir(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	upperFile := filepath.Join(tmp, "containers", "dev", "upper", "keep")
	workFile := filepath.Join(tmp, "containers", "dev", "work", "remove")
	if err := os.MkdirAll(filepath.Dir(upperFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(workFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upperFile, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workFile, []byte("remove"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if _, err := manager.ResolveRootfs(context.Background(), resolveRequest(pod, "container-id")); err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if _, err := os.Stat(upperFile); err != nil {
		t.Fatalf("upper file should remain: %v", err)
	}
	if _, err := os.Stat(workFile); !os.IsNotExist(err) {
		t.Fatalf("work file should be removed, got %v", err)
	}
}

func TestPreparePodDoesNotCleanWorkdir(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	workFile := filepath.Join(tmp, "containers", "dev", "work", "keep-during-reconcile")
	if err := os.MkdirAll(filepath.Dir(workFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workFile, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod: %v", err)
	}

	if _, err := os.Stat(workFile); err != nil {
		t.Fatalf("PreparePod should not clean workdir contents: %v", err)
	}
}

func TestPreparePodDoesNotResetCleanShutdownAfterRelease(t *testing.T) {
	manager, pod, metaDir := releasedTestRootfs(t)
	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod after release: %v", err)
	}

	clean := readCleanShutdown(t, metaDir)
	if !clean.CleanShutdown {
		t.Fatalf("PreparePod should not reset released clean-shutdown state: %+v", clean)
	}
}

func TestResolveRootfsMarksRootfsNotCleanAfterRelease(t *testing.T) {
	manager, pod, metaDir := releasedTestRootfs(t)
	if _, err := manager.ResolveRootfs(context.Background(), resolveRequest(pod, "container-id-2")); err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}

	clean := readCleanShutdown(t, metaDir)
	if clean.CleanShutdown {
		t.Fatalf("ResolveRootfs should mark rootfs not clean before mount: %+v", clean)
	}
}

func releasedTestRootfs(t *testing.T) (*Manager, PodRootfs, string) {
	t.Helper()
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	metaDir := filepath.Join(tmp, "containers", "dev", "meta")

	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod: %v", err)
	}
	if _, err := manager.ReleaseRootfs(context.Background(), apiv1alpha1.ReleaseRootfsRequest{
		ContainerID:   "container-id",
		PodUID:        pod.PodUID,
		ContainerName: "dev",
		MetaDir:       metaDir,
	}); err != nil {
		t.Fatalf("ReleaseRootfs: %v", err)
	}
	return manager, pod, metaDir
}

func TestResolveRootfsSerializesPreparationForSameRootfsPath(t *testing.T) {
	tmp := t.TempDir()
	firstPrepareBlocked := make(chan struct{})
	releaseFirstPrepare := make(chan struct{})
	var nowCalls atomic.Int32
	manager := NewManager(ManagerOptions{
		NodeName: "gpu-node-1",
		Now: func() time.Time {
			if nowCalls.Add(1) == 1 {
				close(firstPrepareBlocked)
				<-releaseFirstPrepare
			}
			return time.Unix(123, 0)
		},
	})
	firstPod := testPodRootfs(tmp)
	secondPod := testPodRootfs(tmp)
	secondPod.PodUID = "pod-uid-2"
	secondPod.PodName = "gpu-dev-2"
	for _, pod := range []PodRootfs{firstPod, secondPod} {
		if err := manager.RegisterPod(context.Background(), pod); err != nil {
			t.Fatalf("RegisterPod %s: %v", pod.PodUID, err)
		}
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.ResolveRootfs(context.Background(), resolveRequest(firstPod, "container-1"))
		firstDone <- err
	}()

	select {
	case <-firstPrepareBlocked:
	case <-time.After(time.Second):
		t.Fatal("first ResolveRootfs did not reach preparation")
	}

	workFile := filepath.Join(tmp, "containers", "dev", "work", "must-not-be-cleaned-yet")
	if err := os.WriteFile(workFile, []byte("keep until first prepare completes"), 0o644); err != nil {
		t.Fatal(err)
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.ResolveRootfs(context.Background(), resolveRequest(secondPod, "container-2"))
		secondDone <- err
	}()

	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second ResolveRootfs: %v", err)
		}
		if _, statErr := os.Stat(workFile); os.IsNotExist(statErr) {
			t.Fatal("second ResolveRootfs cleaned workdir before first preparation completed")
		}
		t.Fatal("second ResolveRootfs completed before first preparation completed")
	case <-time.After(50 * time.Millisecond):
		if _, err := os.Stat(workFile); err != nil {
			t.Fatalf("workdir file should remain while second preparation waits: %v", err)
		}
	}

	close(releaseFirstPrepare)
	for _, result := range []struct {
		name string
		done <-chan error
	}{{name: "first", done: firstDone}, {name: "second", done: secondDone}} {
		select {
		case err := <-result.done:
			if err != nil {
				t.Fatalf("%s ResolveRootfs: %v", result.name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s ResolveRootfs did not finish", result.name)
		}
	}
}

func TestResolveRootfsTargetContainer(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod: %v", err)
	}

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "snapshot-dev",
		PodUID:        pod.PodUID,
		ContainerName: "dev",
		Labels: map[string]string{
			"io.kubernetes.pod.uid":        pod.PodUID,
			"io.kubernetes.container.name": "dev",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.Enabled || resp.FallbackToDefaultOverlay {
		t.Fatalf("expected enabled persistent rootfs, got %+v", resp)
	}
	if resp.UpperDir != filepath.Join(tmp, "containers", "dev", "upper") {
		t.Fatalf("unexpected upper dir: %q", resp.UpperDir)
	}
	if resp.WorkDir != filepath.Join(tmp, "containers", "dev", "work") {
		t.Fatalf("unexpected work dir: %q", resp.WorkDir)
	}
	if resp.MetaDir != filepath.Join(tmp, "containers", "dev", "meta") {
		t.Fatalf("unexpected meta dir: %q", resp.MetaDir)
	}
}

func TestReleaseRootfsWritesCleanShutdown(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}
	if err := manager.PreparePod(context.Background(), pod.PodUID); err != nil {
		t.Fatalf("PreparePod: %v", err)
	}

	resp, err := manager.ReleaseRootfs(context.Background(), apiv1alpha1.ReleaseRootfsRequest{
		ContainerID:   "container-id",
		PodUID:        pod.PodUID,
		ContainerName: "dev",
	})
	if err != nil {
		t.Fatalf("ReleaseRootfs: %v", err)
	}
	if !resp.Released {
		t.Fatalf("expected released response: %+v", resp)
	}

	cleanPath := filepath.Join(tmp, "containers", "dev", "meta", "clean-shutdown.json")
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		t.Fatalf("read clean-shutdown: %v", err)
	}
	var clean apiv1alpha1.CleanShutdownInfo
	if err := json.Unmarshal(data, &clean); err != nil {
		t.Fatalf("unmarshal clean-shutdown: %v", err)
	}
	if !clean.CleanShutdown || clean.ContainerID != "container-id" || clean.PodUID != pod.PodUID || clean.ContainerName != "dev" {
		t.Fatalf("unexpected clean-shutdown state: %+v", clean)
	}
}

func TestReleaseRootfsCanUseMetaDirFromWrapperState(t *testing.T) {
	tmp := t.TempDir()
	metaDir := filepath.Join(tmp, "meta")
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})

	resp, err := manager.ReleaseRootfs(context.Background(), apiv1alpha1.ReleaseRootfsRequest{
		ContainerID:   "container-id",
		PodUID:        "pod-uid-1",
		ContainerName: "dev",
		MetaDir:       metaDir,
	})
	if err != nil {
		t.Fatalf("ReleaseRootfs: %v", err)
	}
	if !resp.Released {
		t.Fatalf("expected released response: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(metaDir, "clean-shutdown.json")); err != nil {
		t.Fatalf("expected clean-shutdown file: %v", err)
	}
}

func TestResolveRootfsTargetContainerFromCRIStyleLabels(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "snapshot-dev",
		Labels: map[string]string{
			"io.kubernetes.cri.sandbox-uid":    pod.PodUID,
			"io.kubernetes.cri.container-name": "dev",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.Enabled || resp.UpperDir != filepath.Join(tmp, "containers", "dev", "upper") {
		t.Fatalf("expected enabled persistent rootfs from CRI labels, got %+v", resp)
	}
}

func TestResolveRootfsTargetContainerFromPodNameWhenUIDMissing(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "runtime-wrapper-dev",
		Namespace:     pod.Namespace,
		PodName:       pod.PodName,
		ContainerName: "dev",
		Labels: map[string]string{
			"ecloud.com/persistent-rootfs": "true",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.Enabled || resp.UpperDir != filepath.Join(tmp, "containers", "dev", "upper") {
		t.Fatalf("expected enabled persistent rootfs from pod name index, got %+v", resp)
	}
}

func TestResolveRootfsPodNameIndexUsesLatestUID(t *testing.T) {
	oldTmp := t.TempDir()
	newTmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	oldPod := testPodRootfs(oldTmp)
	newPod := testPodRootfs(newTmp)
	newPod.PodUID = "pod-uid-2"
	if err := manager.RegisterPod(context.Background(), oldPod); err != nil {
		t.Fatalf("RegisterPod old: %v", err)
	}
	if err := manager.RegisterPod(context.Background(), newPod); err != nil {
		t.Fatalf("RegisterPod new: %v", err)
	}

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "runtime-wrapper-dev",
		Namespace:     newPod.Namespace,
		PodName:       newPod.PodName,
		ContainerName: "dev",
		Labels: map[string]string{
			"ecloud.com/persistent-rootfs": "true",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if resp.UpperDir != filepath.Join(newTmp, "containers", "dev", "upper") {
		t.Fatalf("expected latest pod UID mapping, got %+v", resp)
	}
}

func TestPrunePodsRemovesStalePodNameIndex(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1", RegistrationWait: -1})
	oldPod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), oldPod); err != nil {
		t.Fatalf("RegisterPod old: %v", err)
	}

	manager.PrunePods(map[string]struct{}{"pod-uid-2": {}})

	_, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "runtime-wrapper-dev",
		Namespace:     oldPod.Namespace,
		PodName:       oldPod.PodName,
		ContainerName: "dev",
		Labels: map[string]string{
			"ecloud.com/persistent-rootfs": "true",
		},
	})
	if err == nil {
		t.Fatalf("expected stale pod name index to be removed")
	}
}

func TestResolveRootfsFallbackForSandboxAndSidecar(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	pod.Containers = append(pod.Containers, "logger")
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	tests := []apiv1alpha1.ResolveRootfsRequest{
		{PodUID: pod.PodUID, ContainerName: "POD", Labels: map[string]string{"io.kubernetes.container.type": "sandbox"}},
		{PodUID: pod.PodUID, ContainerName: "logger"},
	}
	for _, req := range tests {
		resp, err := manager.ResolveRootfs(context.Background(), req)
		if err != nil {
			t.Fatalf("ResolveRootfs(%+v): %v", req, err)
		}
		if !resp.FallbackToDefaultOverlay || resp.Enabled {
			t.Fatalf("expected fallback for %+v, got %+v", req, resp)
		}
	}
}

func TestResolveRootfsFallbackForSnapshotPrefixedSandboxLabel(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "sandbox-snapshot",
		Labels: map[string]string{
			"containerd.io/snapshot/io.kubernetes.cri.container-type": "sandbox",
			"containerd.io/snapshot/io.kubernetes.cri.sandbox-uid":    pod.PodUID,
			rootfs.SnapshotLabelEnabled:                               "true",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.FallbackToDefaultOverlay || resp.Enabled {
		t.Fatalf("expected sandbox fallback, got %+v", resp)
	}
}

func TestResolveRootfsFailClosedForUnidentifiedBusinessContainer(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})
	pod := testPodRootfs(tmp)
	if err := manager.RegisterPod(context.Background(), pod); err != nil {
		t.Fatalf("RegisterPod: %v", err)
	}

	_, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "snapshot-unknown",
		PodUID:      pod.PodUID,
		Labels: map[string]string{
			"ecloud.com/persistent-rootfs": "true",
		},
	})
	if err == nil {
		t.Fatalf("expected fail-closed error")
	}
}

func TestResolveRootfsFallsBackForUnregisteredOrdinaryCRIContainerIdentity(t *testing.T) {
	manager := NewManager(ManagerOptions{
		NodeName:         "gpu-node-1",
		RegistrationWait: -1,
	})

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "snapshot-dev",
		PodUID:        "pod-uid-1",
		ContainerName: "dev",
		Labels: map[string]string{
			"io.kubernetes.pod.uid":        "pod-uid-1",
			"io.kubernetes.container.name": "dev",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.FallbackToDefaultOverlay || resp.Enabled {
		t.Fatalf("expected ordinary CRI container fallback, got %+v", resp)
	}
}

func TestResolveRootfsFailClosedForUnregisteredRootPVCRIContainerIdentity(t *testing.T) {
	manager := NewManager(ManagerOptions{
		NodeName:         "gpu-node-1",
		RegistrationWait: -1,
	})

	_, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "snapshot-dev",
		PodUID:        "pod-uid-1",
		ContainerName: "dev",
		Labels: map[string]string{
			"io.kubernetes.pod.uid":        "pod-uid-1",
			"io.kubernetes.container.name": "dev",
			rootfs.AnnotationEnabled:       "true",
		},
	})
	if err == nil {
		t.Fatalf("expected fail-closed error")
	}
}

func TestResolveRootfsWaitsForPodRegistration(t *testing.T) {
	tmp := t.TempDir()
	manager := NewManager(ManagerOptions{
		NodeName:         "gpu-node-1",
		RegistrationWait: 250 * time.Millisecond,
		RegistrationPoll: time.Millisecond,
	})
	pod := testPodRootfs(tmp)

	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = manager.RegisterPod(context.Background(), pod)
	}()

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey:   "snapshot-dev",
		PodUID:        pod.PodUID,
		ContainerName: "dev",
		Labels: map[string]string{
			"io.kubernetes.pod.uid":        pod.PodUID,
			"io.kubernetes.container.name": "dev",
			rootfs.AnnotationEnabled:       "true",
		},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.Enabled || resp.UpperDir != filepath.Join(tmp, "containers", "dev", "upper") {
		t.Fatalf("expected enabled persistent rootfs after registration wait, got %+v", resp)
	}
}

func TestResolveRootfsFailClosedForSnapshotPrefixedEnabledLabel(t *testing.T) {
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})

	_, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "snapshot-unknown",
		Labels: map[string]string{
			rootfs.SnapshotLabelEnabled: "true",
		},
	})
	if err == nil {
		t.Fatalf("expected fail-closed error")
	}
}

func TestResolveRootfsFallsBackForUnlabeledCRIContainerSnapshot(t *testing.T) {
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "100f07b645b2feb08db349bb62c3340ad29745756110c3ad345f058d0d2c9375",
		Labels:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.FallbackToDefaultOverlay || resp.Enabled {
		t.Fatalf("expected unlabeled CRI snapshot fallback, got %+v", resp)
	}
}

func TestResolveRootfsFallsBackForPrefixedCRIContainerSnapshot(t *testing.T) {
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "k8s.io/170/17373154a223f55f5d554bcf72a1f12406f7ef52732a08f5a86ccbe8df117afd",
		Labels:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.FallbackToDefaultOverlay || resp.Enabled {
		t.Fatalf("expected prefixed CRI snapshot fallback, got %+v", resp)
	}
}

func TestResolveRootfsFallsBackForImageUnpackWithoutIdentity(t *testing.T) {
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "extract-643647875-iXU_",
		Labels:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.FallbackToDefaultOverlay {
		t.Fatalf("expected image unpack fallback, got %+v", resp)
	}
}

func TestResolveRootfsFallsBackForPrefixedImageUnpackWithoutIdentity(t *testing.T) {
	manager := NewManager(ManagerOptions{NodeName: "gpu-node-1"})

	resp, err := manager.ResolveRootfs(context.Background(), apiv1alpha1.ResolveRootfsRequest{
		SnapshotKey: "k8s.io/168/extract-73032384-9m4m sha256:a8cf902a30f582f5ff911228dafb1f3da70f8a7d65359c656f55abf577584a14",
		Labels:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("ResolveRootfs: %v", err)
	}
	if !resp.FallbackToDefaultOverlay {
		t.Fatalf("expected image unpack fallback, got %+v", resp)
	}
}

func TestDiscoverPVCHostPathFindsKubeletVolumeMount(t *testing.T) {
	tmp := t.TempDir()
	mountPath := filepath.Join(tmp, "pods", "pod-uid-1", "volumes", "kubernetes.io~csi", "rootfs-pvc", "mount")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverPVCHostPath(tmp, "pod-uid-1", "rootfs-pvc", "gpu-dev-rootfs-pvc")
	if err != nil {
		t.Fatalf("DiscoverPVCHostPath: %v", err)
	}
	if got != mountPath {
		t.Fatalf("unexpected path: got %q want %q", got, mountPath)
	}
}

func TestDiscoverPVCHostPathFindsLocalPVVolumeDir(t *testing.T) {
	tmp := t.TempDir()
	volumePath := filepath.Join(tmp, "pods", "pod-uid-1", "volumes", "kubernetes.io~local-volume", "gpu-dev-rootfs-localpv")
	if err := os.MkdirAll(volumePath, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverPVCHostPath(tmp, "pod-uid-1", "rootfs-pvc", "gpu-dev-rootfs-pvc")
	if err != nil {
		t.Fatalf("DiscoverPVCHostPath: %v", err)
	}
	if got != volumePath {
		t.Fatalf("unexpected path: got %q want %q", got, volumePath)
	}
}

func TestDiscoverPVCHostPathReportsAmbiguousFallbackCandidates(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"data-pvc", "logs-pvc"} {
		mountPath := filepath.Join(tmp, "pods", "pod-uid-1", "volumes", "kubernetes.io~csi", name, "mount")
		if err := os.MkdirAll(mountPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := DiscoverPVCHostPath(tmp, "pod-uid-1", "rootfs-pvc", "rootfs-claim")
	if err == nil || !strings.Contains(err.Error(), "found 2 PVC volume candidates") {
		t.Fatalf("expected ambiguous fallback error, got %v", err)
	}
}

func TestDiscoverPVCHostPathIgnoresNonPVCPluginFallbacks(t *testing.T) {
	tmp := t.TempDir()
	secretPath := filepath.Join(tmp, "pods", "pod-uid-1", "volumes", "kubernetes.io~secret", "rootfs-secret")
	if err := os.MkdirAll(secretPath, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverPVCHostPath(tmp, "pod-uid-1", "rootfs-pvc", "rootfs-claim")
	if err == nil || !strings.Contains(err.Error(), "PVC host path is not mounted yet") {
		t.Fatalf("expected not mounted error, got %v", err)
	}
}

func testPodRootfs(pvcHostPath string) PodRootfs {
	return PodRootfs{
		Namespace: "ai-dev",
		PodName:   "gpu-dev",
		PodUID:    "pod-uid-1",
		NodeName:  "gpu-node-1",
		Containers: []string{
			"dev",
		},
		PVCHostPaths: map[string]string{
			"rootfs-pvc": pvcHostPath,
		},
		Specs: []rootfs.ContainerRootfsSpec{{
			ContainerName:   "dev",
			VolumeName:      "rootfs-pvc",
			PVCName:         "gpu-dev-rootfs",
			Path:            "containers/dev",
			Mode:            rootfs.DefaultMode,
			BaseImagePolicy: rootfs.BaseImagePolicyStrict,
		}},
	}
}

func resolveRequest(pod PodRootfs, containerID string) apiv1alpha1.ResolveRootfsRequest {
	return apiv1alpha1.ResolveRootfsRequest{
		ContainerID:   containerID,
		SnapshotKey:   "snapshot-" + containerID,
		PodUID:        pod.PodUID,
		ContainerName: "dev",
		Labels: map[string]string{
			"io.kubernetes.pod.uid":        pod.PodUID,
			"io.kubernetes.container.name": "dev",
		},
	}
}

func readCleanShutdown(t *testing.T, metaDir string) apiv1alpha1.CleanShutdownInfo {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(metaDir, "clean-shutdown.json"))
	if err != nil {
		t.Fatalf("read clean-shutdown: %v", err)
	}
	var clean apiv1alpha1.CleanShutdownInfo
	if err := json.Unmarshal(data, &clean); err != nil {
		t.Fatalf("unmarshal clean-shutdown: %v", err)
	}
	return clean
}

func emptyMountInfo(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mountInfoForPath(t *testing.T, path string) string {
	t.Helper()
	mountInfo := emptyMountInfo(t)
	line := "36 25 0:32 / " + path + " rw,relatime - ext4 /dev/test rw\n"
	if err := os.WriteFile(mountInfo, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return mountInfo
}
