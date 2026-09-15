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
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func TestReconcilerReacquiresCreatedLeaseBeforeSuccess(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	writeRecoveryState(t, recoveryStateInput{runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseCreated})
	abandonLease(t, old, lease.LeaseID)

	recovered := NewLeaseManager("node-1", nil)
	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: recovered, Processes: fakeProcessChecker{}, Cleaner: &fakeRecoveryCleaner{}}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	competitor := NewLeaseManager("node-1", nil)
	_, err := competitor.Acquire(context.Background(), recoveryAcquireRequest(rootPath, "other-container"))
	if err == nil || !containsError(err, "persistent_upper_in_use") {
		t.Fatalf("expected recovered flock to remain held, got %v", err)
	}
}

func TestReconcilerCleansStoppedDelegatedRootfs(t *testing.T) {
	runtimeRoot, old, state := prepareDelegatedRecoveryState(t)
	abandonLease(t, old, state.LeaseID)

	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{
		RuntimeRoot: runtimeRoot,
		Leases:      NewLeaseManager("node-1", nil),
		Processes:   fakeProcessChecker{},
		Runtime:     fakeRuntimeStateChecker{},
		Cleaner:     cleaner,
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if cleaner.calls != 1 {
		t.Fatalf("expected delegated rootfs cleanup, got %d calls", cleaner.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, state.ContainerID)); !os.IsNotExist(err) {
		t.Fatalf("delegated state was not removed: %v", err)
	}
}

func TestReconcilerRetainsRunningDelegatedRootfs(t *testing.T) {
	runtimeRoot, old, state := prepareDelegatedRecoveryState(t)
	abandonLease(t, old, state.LeaseID)

	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{
		RuntimeRoot: runtimeRoot,
		Leases:      NewLeaseManager("node-1", nil),
		Processes:   fakeProcessChecker{},
		Runtime:     fakeRuntimeStateChecker{running: true},
		Cleaner:     cleaner,
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if cleaner.calls != 0 {
		t.Fatalf("running delegated rootfs was cleaned")
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, state.ContainerID)); err != nil {
		t.Fatalf("running delegated state was removed: %v", err)
	}
}

func TestReconcilerCleansDelegatedStateWhenLeaseMetadataIsGone(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	state := delegatedRecoveryState(runtimeRoot, t.TempDir(), "missing-lease")
	if err := rootfsstate.Save(runtimeRoot, state.ContainerID, state); err != nil {
		t.Fatal(err)
	}

	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{
		RuntimeRoot: runtimeRoot,
		Leases:      NewLeaseManager("node-1", nil),
		Processes:   fakeProcessChecker{},
		Runtime:     fakeRuntimeStateChecker{},
		Cleaner:     cleaner,
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if cleaner.calls != 1 {
		t.Fatalf("expected stale delegated cleanup, got %d calls", cleaner.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, state.ContainerID)); !os.IsNotExist(err) {
		t.Fatalf("stale delegated state was not removed: %v", err)
	}
}

func TestReconcilerCleansDeadOwnerOnlyAfterAllLeasesRecovered(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	owner := rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}
	writeRecoveryState(t, recoveryStateInput{runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseAcquired, owner: owner})
	abandonLease(t, old, lease.LeaseID)

	var order []string
	cleaner := &fakeRecoveryCleaner{order: &order}
	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{}, Cleaner: cleaner}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"cleanup"}) {
		t.Fatalf("unexpected cleanup order: %v", order)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("reconciled state was not removed: %v", err)
	}
	if _, err := NewLeaseManager("node-1", nil).Acquire(context.Background(), recoveryAcquireRequest(rootPath, "new-container")); err != nil {
		t.Fatalf("released flock was not reusable: %v", err)
	}
}

func TestReconcilerRetainsLeaseForLiveOwner(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	owner := rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}
	writeRecoveryState(t, recoveryStateInput{runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseMounted, owner: owner})
	abandonLease(t, old, lease.LeaseID)

	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{alive: map[rootfsstate.ProcessIdentity]bool{owner: true}}, Cleaner: cleaner}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if cleaner.calls != 0 {
		t.Fatalf("live owner was cleaned")
	}
}

func TestReconcilerCleanupFailureKeepsBarrierClosed(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	writeRecoveryState(t, recoveryStateInput{runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseDeleting})
	abandonLease(t, old, lease.LeaseID)

	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{}, Cleaner: &fakeRecoveryCleaner{err: errors.New("mount state uncertain")}}
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("expected reconciliation barrier failure")
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); err != nil {
		t.Fatalf("uncertain state should remain: %v", err)
	}
}

func TestReconcilerRetriesCleanupAfterCreateAttemptWasRevoked(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	if err := old.MarkMounted(context.Background(), lease.LeaseID, "/run/rootpv/state.json"); err != nil {
		t.Fatal(err)
	}
	token, err := old.BeginCreate(context.Background(), lease.LeaseID, "container-id", rootfsstate.ProcessIdentity{PID: 101, StartTime: 202})
	if err != nil {
		t.Fatal(err)
	}
	writeRecoveryState(t, recoveryStateInput{
		runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseCreatingPending,
		owner: rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}, attemptStatus: rootfsstate.AttemptPending, attemptToken: token,
	})
	abandonLease(t, old, lease.LeaseID)

	cleaner := &fakeRecoveryCleaner{err: errors.New("mount state uncertain")}
	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{}, Cleaner: cleaner}
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("expected first reconciliation to preserve uncertain state")
	}
	cleaner.err = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after cleanup recovery: %v", err)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("recovered state was not removed: %v", err)
	}
}

func TestReconcilerCleansDeadRegisteredDelegate(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	wrapper := rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}
	delegate := rootfsstate.ProcessIdentity{PID: 102, StartTime: 203}
	if err := old.MarkMounted(context.Background(), lease.LeaseID, "/run/rootpv/state.json"); err != nil {
		t.Fatal(err)
	}
	token, err := old.BeginCreate(context.Background(), lease.LeaseID, "container-id", wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RegisterDelegate(context.Background(), lease.LeaseID, token, delegate); err != nil {
		t.Fatal(err)
	}
	writeRecoveryState(t, recoveryStateInput{
		runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseCreatingRegistered,
		owner: wrapper, delegate: delegate, attemptStatus: rootfsstate.AttemptRegistered, attemptToken: token,
	})
	abandonLease(t, old, lease.LeaseID)

	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{}, Cleaner: cleaner}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if cleaner.calls != 1 {
		t.Fatalf("expected one cleanup for dead delegate, got %d", cleaner.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("reconciled state was not removed: %v", err)
	}
}

func TestReconcilerRetriesStateRemovalAfterLeaseReleased(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	old := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, old, rootPath, "container-id")
	writeRecoveryState(t, recoveryStateInput{runtimeRoot: runtimeRoot, lease: lease, rootPath: rootPath, phase: rootfsstate.PhaseDeleting})
	abandonLease(t, old, lease.LeaseID)

	removeFailed := false
	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{
		RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{}, Cleaner: cleaner,
		removeAll: func(path string) error {
			if !removeFailed {
				removeFailed = true
				return errors.New("runtime state directory is busy")
			}
			return os.RemoveAll(path)
		},
	}
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("expected first state removal to fail")
	}
	if cleaner.calls != 1 {
		t.Fatalf("expected cleanup before first release, got %d calls", cleaner.calls)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after state removal recovery: %v", err)
	}
	if cleaner.calls != 1 {
		t.Fatalf("released state should not be cleaned twice, got %d calls", cleaner.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("released runtime state was not removed: %v", err)
	}
}

func TestReconcilerCleansReleasedStateDespiteOtherRecoveryFailure(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	releasedRoot := filepath.Join(t.TempDir(), "released")
	brokenRoot := filepath.Join(t.TempDir(), "broken")
	old := NewLeaseManager("node-1", nil)
	releasedLease := acquireRecoveryLease(t, old, releasedRoot, "released-container")
	writeRecoveryState(t, recoveryStateInput{
		runtimeRoot: runtimeRoot, containerID: "released-container", lease: releasedLease, rootPath: releasedRoot, phase: rootfsstate.PhaseDeleting,
	})
	if _, err := old.Release(context.Background(), releasedLease.LeaseID); err != nil {
		t.Fatal(err)
	}

	brokenLease := acquireRecoveryLease(t, old, brokenRoot, "broken-container")
	writeRecoveryState(t, recoveryStateInput{
		runtimeRoot: runtimeRoot, containerID: "broken-container", lease: brokenLease, rootPath: brokenRoot, phase: rootfsstate.PhaseDeleting,
	})
	if err := os.WriteFile(filepath.Join(brokenRoot, "meta", "lease.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	abandonLease(t, old, brokenLease.LeaseID)

	reconciler := Reconciler{RuntimeRoot: runtimeRoot, Leases: NewLeaseManager("node-1", nil), Processes: fakeProcessChecker{}, Cleaner: &fakeRecoveryCleaner{}}
	err := reconciler.Reconcile(context.Background())
	if err == nil || !containsError(err, "recover lease for broken-container") {
		t.Fatalf("expected broken lease recovery error, got %v", err)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "released-container")); !os.IsNotExist(err) {
		t.Fatalf("released state was blocked by unrelated recovery failure: %v", err)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "broken-container")); err != nil {
		t.Fatalf("broken state should remain for later recovery: %v", err)
	}
}

type fakeProcessChecker struct {
	alive map[rootfsstate.ProcessIdentity]bool
}

type fakeRuntimeStateChecker struct {
	running bool
	err     error
}

func (c fakeRuntimeStateChecker) Running(context.Context, rootfsstate.State) (bool, error) {
	return c.running, c.err
}

func (c fakeProcessChecker) Alive(identity rootfsstate.ProcessIdentity) bool {
	return c.alive[identity]
}

type fakeRecoveryCleaner struct {
	order *[]string
	calls int
	err   error
}

func (c *fakeRecoveryCleaner) Cleanup(context.Context, rootfsstate.State) error {
	c.calls++
	if c.order != nil {
		*c.order = append(*c.order, "cleanup")
	}
	return c.err
}

func prepareDelegatedRecoveryState(t *testing.T) (string, *LeaseManager, rootfsstate.State) {
	t.Helper()
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootPath := filepath.Join(t.TempDir(), "persistent")
	manager := NewLeaseManager("node-1", nil)
	lease := acquireRecoveryLease(t, manager, rootPath, "container-id")
	state := delegatedRecoveryState(runtimeRoot, rootPath, lease.LeaseID)
	if err := rootfsstate.Save(runtimeRoot, state.ContainerID, state); err != nil {
		t.Fatalf("Save delegated state: %v", err)
	}
	return runtimeRoot, manager, state
}

func delegatedRecoveryState(runtimeRoot, rootPath, leaseID string) rootfsstate.State {
	return rootfsstate.State{
		FormatVersion:        rootfsstate.FormatDelegationV1,
		ContainerID:          "container-id",
		LeaseID:              leaseID,
		Phase:                rootfsstate.PhaseCreated,
		MetaDir:              filepath.Join(rootPath, "meta"),
		MergedDir:            filepath.Join(runtimeRoot, "container-id", "merged"),
		RootfsEnabled:        true,
		DelegateBinary:       "/delegate/runc",
		RuntimeAdapterID:     "runc-v2",
		RuntimeAdapterMode:   "Explicit",
		RootfsHandler:        "persistent-rootfs-runc-v2",
		RuntimeType:          "io.containerd.runc.v2",
		ClusterAdapterDigest: "sha256:cluster",
		NodeResolvedDigest:   "sha256:node",
	}
}

func acquireRecoveryLease(t *testing.T, manager *LeaseManager, rootPath, containerID string) Lease {
	t.Helper()
	lease, err := manager.Acquire(context.Background(), recoveryAcquireRequest(rootPath, containerID))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return lease
}

func recoveryAcquireRequest(rootPath, containerID string) LeaseAcquireRequest {
	return LeaseAcquireRequest{
		RootPath: rootPath, ContainerID: containerID, Snapshotter: "overlayfs",
		RootfsChainID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseImagePolicy: "Strict",
	}
}

func abandonLease(t *testing.T, manager *LeaseManager, leaseID string) {
	t.Helper()
	manager.mu.Lock()
	lease := manager.held[leaseID]
	delete(manager.held, leaseID)
	manager.mu.Unlock()
	if lease == nil {
		t.Fatal("lease is not held")
	}
	if err := unix.Flock(int(lease.file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := lease.file.Close(); err != nil {
		t.Fatal(err)
	}
}

type recoveryStateInput struct {
	runtimeRoot   string
	containerID   string
	lease         Lease
	rootPath      string
	phase         string
	owner         rootfsstate.ProcessIdentity
	delegate      rootfsstate.ProcessIdentity
	attemptStatus string
	attemptToken  string
}

func writeRecoveryState(t *testing.T, input recoveryStateInput) {
	t.Helper()
	containerID := input.containerID
	if containerID == "" {
		containerID = "container-id"
	}
	state := rootfsstate.State{
		FormatVersion: rootfsstate.FormatFlatV1, ContainerID: containerID, LeaseID: input.lease.LeaseID, Phase: input.phase,
		Snapshotter: "overlayfs", RootfsChainID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetaDir: filepath.Join(input.rootPath, "meta"), MergedDir: filepath.Join(input.runtimeRoot, containerID, "merged"), Wrapper: input.owner,
		Delegate: input.delegate, AttemptStatus: input.attemptStatus, AttemptToken: input.attemptToken,
	}
	if err := rootfsstate.Save(input.runtimeRoot, state.ContainerID, state); err != nil {
		t.Fatalf("Save state: %v", err)
	}
}

func containsError(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}
