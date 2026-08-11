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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

const testChainID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBaseMetadataInitializesEmptyUpper(t *testing.T) {
	root := newRootfsTarget(t)
	if err := ensureBaseMetadata(root, "overlayfs", testChainID, "Strict"); err != nil {
		t.Fatalf("ensureBaseMetadata: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "meta", "base.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got BaseMetadata
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.FormatVersion != BaseMetadataFormatV1 || got.Snapshotter != "overlayfs" || got.RootfsChainID != testChainID || got.BaseImagePolicy != "Strict" {
		t.Fatalf("unexpected base metadata: %+v", got)
	}
}

func TestRootfsLeaseHoldsFlockAfterAcquireAndProtectsWorkdir(t *testing.T) {
	root := newRootfsTarget(t)
	first := NewLeaseManager("node-1", func() time.Time { return time.Unix(100, 0) })
	second := NewLeaseManager("node-1", func() time.Time { return time.Unix(101, 0) })
	request := testLeaseAcquireRequest(root, "container-1")

	lease, err := first.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	marker := filepath.Join(root, "work", "must-remain")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Acquire(context.Background(), testLeaseAcquireRequest(root, "container-2")); err == nil || !strings.Contains(err.Error(), "persistent_upper_in_use") {
		t.Fatalf("expected flock conflict, got %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("blocked acquire cleaned workdir: %v", err)
	}
	if lease.LeaseID == "" {
		t.Fatal("lease ID is empty")
	}
	data, err := os.ReadFile(filepath.Join(root, "meta", "lease.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata LeaseMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.LeaseID != lease.LeaseID || metadata.Phase != LeasePhaseAcquired || metadata.ContainerID != "container-1" {
		t.Fatalf("unexpected lease metadata: %+v", metadata)
	}
	if _, err := first.Release(context.Background(), lease.LeaseID); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestRootfsLeaseCreateAttemptLifecycle(t *testing.T) {
	root := newRootfsTarget(t)
	manager := NewLeaseManager("node-1", func() time.Time { return time.Unix(100, 0) })
	lease, err := manager.Acquire(context.Background(), testLeaseAcquireRequest(root, "container-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginCreate(context.Background(), lease.LeaseID, "container-1", rootfsstate.ProcessIdentity{PID: 100, StartTime: 1000}); err == nil || !strings.Contains(err.Error(), "lease_not_mounted") {
		t.Fatalf("begin must reject an unmounted lease, got %v", err)
	}
	if err := manager.MarkMounted(context.Background(), lease.LeaseID, "/run/rootpv/rootfs/container-1/state.json"); err != nil {
		t.Fatalf("MarkMounted: %v", err)
	}
	token, err := manager.BeginCreate(context.Background(), lease.LeaseID, "container-1", rootfsstate.ProcessIdentity{PID: 100, StartTime: 1000})
	if err != nil {
		t.Fatalf("BeginCreate: %v", err)
	}
	if token == "" {
		t.Fatal("attempt token is empty")
	}
	if err := manager.RegisterDelegate(context.Background(), lease.LeaseID, "wrong-token", rootfsstate.ProcessIdentity{PID: 101, StartTime: 1001}); err == nil || !strings.Contains(err.Error(), "create_attempt_token_invalid") {
		t.Fatalf("expected wrong token rejection, got %v", err)
	}
	if err := manager.RegisterDelegate(context.Background(), lease.LeaseID, token, rootfsstate.ProcessIdentity{PID: 101, StartTime: 1001}); err != nil {
		t.Fatalf("RegisterDelegate: %v", err)
	}
	if err := manager.CommitCreate(context.Background(), lease.LeaseID, token); err != nil {
		t.Fatalf("CommitCreate: %v", err)
	}
	metadata := readLeaseMetadata(t, root)
	if metadata.Phase != LeasePhaseCreated || metadata.AttemptStatus != AttemptStatusRegistered || metadata.DelegatePID != 101 {
		t.Fatalf("unexpected committed lease: %+v", metadata)
	}

	released, err := manager.Release(context.Background(), lease.LeaseID)
	if err != nil || !released {
		t.Fatalf("Release: released=%v err=%v", released, err)
	}
	released, err = manager.Release(context.Background(), lease.LeaseID)
	if err != nil || !released {
		t.Fatalf("idempotent Release: released=%v err=%v", released, err)
	}
}

func TestRootfsLeaseRevokesPendingAttemptBeforeLateRegistration(t *testing.T) {
	root := newRootfsTarget(t)
	manager := NewLeaseManager("node-1", time.Now)
	lease, err := manager.Acquire(context.Background(), testLeaseAcquireRequest(root, "container-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkMounted(context.Background(), lease.LeaseID, "/run/state.json"); err != nil {
		t.Fatal(err)
	}
	token, err := manager.BeginCreate(context.Background(), lease.LeaseID, "container-1", rootfsstate.ProcessIdentity{PID: 100, StartTime: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RevokeCreate(context.Background(), lease.LeaseID, token); err != nil {
		t.Fatalf("RevokeCreate: %v", err)
	}
	if err := manager.RevokeCreate(context.Background(), lease.LeaseID, token); err != nil {
		t.Fatalf("repeated RevokeCreate: %v", err)
	}
	if err := manager.RegisterDelegate(context.Background(), lease.LeaseID, token, rootfsstate.ProcessIdentity{PID: 101, StartTime: 1001}); err == nil || !strings.Contains(err.Error(), "create_attempt_revoked") {
		t.Fatalf("late delegate should be rejected, got %v", err)
	}
	if _, err := manager.Release(context.Background(), lease.LeaseID); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseLeaseResourcesClosesAfterUnlockFailure(t *testing.T) {
	closed := false
	err := releaseLeaseResources(
		func() error { return errors.New("unlock failed") },
		func() error {
			closed = true
			return errors.New("close failed")
		},
	)
	if !closed {
		t.Fatal("file close was skipped after unlock failure")
	}
	if err == nil || !strings.Contains(err.Error(), "unlock rootfs lease") || !strings.Contains(err.Error(), "close rootfs lease") {
		t.Fatalf("expected both release errors, got %v", err)
	}
}

func TestRootfsLeaseReleaseRollsBackMetadataAfterWriteFailure(t *testing.T) {
	manager := NewLeaseManager("node-1", nil)
	file, err := os.CreateTemp(t.TempDir(), "lease-lock")
	if err != nil {
		t.Fatal(err)
	}
	leaseID := "lease-write-failure"
	manager.mu.Lock()
	manager.held[leaseID] = &heldLease{
		file: file, rootPath: filepath.Join(t.TempDir(), "missing-root"),
		metadata: LeaseMetadata{LeaseID: leaseID, Phase: LeasePhaseAcquired},
	}
	manager.mu.Unlock()

	if _, err := manager.Release(context.Background(), leaseID); err == nil {
		t.Fatal("expected metadata write failure")
	}
	manager.mu.Lock()
	got := manager.held[leaseID].metadata
	manager.mu.Unlock()
	if got.Phase != LeasePhaseAcquired {
		t.Fatalf("in-memory lease metadata was not rolled back: %+v", got)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReleasedLeaseHistoryIsBounded(t *testing.T) {
	manager := NewLeaseManager("node-1", nil)
	manager.mu.Lock()
	for i := 0; i < releasedLeaseHistoryLimit+1; i++ {
		manager.rememberReleasedLocked(fmt.Sprintf("lease-%d", i))
	}
	manager.mu.Unlock()
	if len(manager.released) != releasedLeaseHistoryLimit || len(manager.releasedOrder) != releasedLeaseHistoryLimit {
		t.Fatalf("released lease history is not bounded: map=%d queue=%d", len(manager.released), len(manager.releasedOrder))
	}
}

func TestBaseMetadataRejectsLegacyUpperWithoutMetadata(t *testing.T) {
	root := newRootfsTarget(t)
	if err := os.WriteFile(filepath.Join(root, "upper", "user-data"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := ensureBaseMetadata(root, "overlayfs", testChainID, "Strict")
	if err == nil || !strings.Contains(err.Error(), "legacy_upper_without_metadata") {
		t.Fatalf("expected legacy upper rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "upper", "user-data")); err != nil {
		t.Fatalf("upper data was modified: %v", err)
	}
}

func TestBaseMetadataRejectsInvalidAndMismatchedState(t *testing.T) {
	tests := []struct {
		name       string
		metadata   string
		wantReason string
	}{
		{name: "malformed", metadata: "{", wantReason: "base_metadata_invalid"},
		{name: "unsupported version", metadata: `{"formatVersion":"v2","snapshotter":"overlayfs","rootfsChainID":"` + testChainID + `","baseImagePolicy":"Strict"}`, wantReason: "base_metadata_version_unsupported"},
		{name: "unsupported policy", metadata: `{"formatVersion":"flat-overlay-lowerdirs-v1","snapshotter":"overlayfs","rootfsChainID":"` + testChainID + `","baseImagePolicy":"Rebase"}`, wantReason: "base_policy_unsupported"},
		{name: "chain mismatch", metadata: `{"formatVersion":"flat-overlay-lowerdirs-v1","snapshotter":"overlayfs","rootfsChainID":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","baseImagePolicy":"Strict"}`, wantReason: "base_image_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootfsTarget(t)
			if err := os.WriteFile(filepath.Join(root, "meta", "base.json"), []byte(tt.metadata), 0o644); err != nil {
				t.Fatal(err)
			}
			err := ensureBaseMetadata(root, "overlayfs", testChainID, "Strict")
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("expected %q, got %v", tt.wantReason, err)
			}
		})
	}
}

func TestBaseMetadataAllowsSameChainAcrossSnapshotInstances(t *testing.T) {
	root := newRootfsTarget(t)
	if err := ensureBaseMetadata(root, "overlayfs", testChainID, "Strict"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "upper", "persistent"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureBaseMetadata(root, "overlayfs", testChainID, "Strict"); err != nil {
		t.Fatalf("same chain should be reusable: %v", err)
	}
}

func newRootfsTarget(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"upper", "work", "meta"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testLeaseAcquireRequest(root, containerID string) LeaseAcquireRequest {
	return LeaseAcquireRequest{
		RootPath:        root,
		ContainerID:     containerID,
		Namespace:       "ai-dev",
		PodName:         "dev-pod",
		PodUID:          "pod-uid-1",
		ContainerName:   "dev",
		Snapshotter:     "overlayfs",
		RootfsChainID:   testChainID,
		BaseImagePolicy: "Strict",
	}
}

func readLeaseMetadata(t *testing.T, root string) LeaseMetadata {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "meta", "lease.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata LeaseMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}
