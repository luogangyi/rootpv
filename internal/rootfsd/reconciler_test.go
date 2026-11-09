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
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"gitcode.com/openFuyao/rootpv/internal/config"
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

func TestReconcilerCleansUntrackedDelegatedRootfs(t *testing.T) {
	runtimeRoot, old, state := prepareDelegatedRecoveryState(t)
	abandonLease(t, old, state.LeaseID)

	cleaner := reconcileDelegatedState(t, runtimeRoot, fakeRuntimeStateChecker{})
	assertDelegatedStateCleaned(t, runtimeRoot, state, cleaner)
}

func TestReconcilerRetainsTrackedDelegatedRootfs(t *testing.T) {
	runtimeRoot, old, state := prepareDelegatedRecoveryState(t)
	abandonLease(t, old, state.LeaseID)

	cleaner := reconcileDelegatedState(t, runtimeRoot, fakeRuntimeStateChecker{present: true})
	if cleaner.calls != 0 {
		t.Fatalf("tracked delegated rootfs was cleaned")
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, state.ContainerID)); err != nil {
		t.Fatalf("tracked delegated state was removed: %v", err)
	}
}

func TestReconcilerCleansDelegatedStateWhenLeaseMetadataIsGone(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	state := delegatedRecoveryState(runtimeRoot, t.TempDir(), "missing-lease")
	if err := rootfsstate.Save(runtimeRoot, state.ContainerID, state); err != nil {
		t.Fatal(err)
	}

	cleaner := reconcileDelegatedState(t, runtimeRoot, fakeRuntimeStateChecker{})
	assertDelegatedStateCleaned(t, runtimeRoot, state, cleaner)
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

func TestReconcilerSkipsLeaseReleasedDuringTaskLookup(t *testing.T) {
	runtimeRoot, leases, state := prepareDelegatedRecoveryState(t)
	cleaner := &fakeRecoveryCleaner{err: errors.New("original namespace is gone")}
	reconciler := Reconciler{
		RuntimeRoot: runtimeRoot, Leases: leases, Cleaner: cleaner,
		Runtime: fakeRuntimeStateChecker{check: func() {
			// The shim completes delete after the recovery snapshot was read.
			if _, err := leases.Release(context.Background(), state.LeaseID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("completed delete caused recovery failure: %v", err)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, state.ContainerID)); !os.IsNotExist(err) {
		t.Fatalf("completed state remains: %v", err)
	}
	if cleaner.calls != 0 {
		t.Fatal("cleaned a runtime whose lease was already released")
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
	present bool
	err     error
	check   func()
}

func (c fakeRuntimeStateChecker) Present(context.Context, rootfsstate.State) (bool, error) {
	if c.check != nil {
		c.check()
	}
	return c.present, c.err
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

func reconcileDelegatedState(
	t *testing.T,
	runtimeRoot string,
	runtime RuntimeStateChecker,
) *fakeRecoveryCleaner {
	t.Helper()
	cleaner := &fakeRecoveryCleaner{}
	reconciler := Reconciler{
		RuntimeRoot: runtimeRoot,
		Leases:      NewLeaseManager("node-1", nil),
		Processes:   fakeProcessChecker{},
		Runtime:     runtime,
		Cleaner:     cleaner,
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return cleaner
}

func assertDelegatedStateCleaned(
	t *testing.T,
	runtimeRoot string,
	state rootfsstate.State,
	cleaner *fakeRecoveryCleaner,
) {
	t.Helper()
	if cleaner.calls != 1 {
		t.Fatalf("expected delegated rootfs cleanup, got %d calls", cleaner.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, state.ContainerID)); !os.IsNotExist(err) {
		t.Fatalf("delegated state was not removed: %v", err)
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

func TestKataRecoveryRetainsLeaseWhileEitherShimOwnerLives(t *testing.T) {
	state := rootfsstate.State{FormatVersion: rootfsstate.FormatShimV1, Phase: rootfsstate.PhaseDeleting, Wrapper: rootfsstate.ProcessIdentity{PID: 1, StartTime: 11}, Shim: rootfsstate.ProcessIdentity{PID: 2, StartTime: 22}}
	for _, owner := range []rootfsstate.ProcessIdentity{state.Wrapper, state.Shim} {
		retained, err := retainRecoveredState(context.Background(), state, fakeProcessChecker{alive: map[rootfsstate.ProcessIdentity]bool{owner: true}}, fakeRuntimeStateChecker{})
		if err != nil || !retained {
			t.Fatalf("live Kata owner lease lost: %v %v", retained, err)
		}
	}
	retained, err := retainRecoveredState(context.Background(), state, fakeProcessChecker{}, fakeRuntimeStateChecker{})
	if err != nil || retained {
		t.Fatalf("dead Kata owners retained forever: %v %v", retained, err)
	}
}

func TestRecoveryRetainsCreateWrapperDuringHandoff(t *testing.T) {
	owner := rootfsstate.ProcessIdentity{PID: 1, StartTime: 11}
	states := []rootfsstate.State{
		{FormatVersion: rootfsstate.FormatFlatV1, Phase: rootfsstate.PhaseCreatingRegistered, Wrapper: owner},
		{FormatVersion: rootfsstate.FormatDelegationV1, Phase: rootfsstate.PhaseCreated, Wrapper: owner, RootfsEnabled: true},
	}
	for _, state := range states {
		processes := fakeProcessChecker{alive: map[rootfsstate.ProcessIdentity]bool{owner: true}}
		retained, err := retainRecoveredState(context.Background(), state, processes, fakeRuntimeStateChecker{})
		if err != nil || !retained {
			t.Fatalf("active create wrapper lost its %s state: %v", state.Phase, err)
		}
	}
}

type runscRecoveryFixture struct {
	proc, ns, nsenter, log string
	state                  rootfsstate.State
}

func newRunscRecoveryFixture(t *testing.T, deleteAction, hostMount string) runscRecoveryFixture {
	t.Helper()
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	ns := filepath.Join(proc, "123/ns")
	if err := os.MkdirAll(ns, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proc, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	mnt := filepath.Join(ns, "mnt")
	if err := os.WriteFile(mnt, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(mnt)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("missing inode")
	}
	mountinfo := filepath.Join(proc, "123/mountinfo")
	if err := os.WriteFile(mountinfo, []byte("1 0 0:1 / /merged rw - overlay overlay rw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "1/mountinfo"), []byte(hostMount), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "order")
	script := `#!/bin/sh
case "$*" in
*umount*) echo unmount >> '` + log + `'; : > '` + mountinfo + `';;
*) printf '%s\n' "$@" > '` + log + `.args'; echo delete >> '` + log + `'; ` + strings.ReplaceAll(deleteAction, "NAMESPACE", ns) + `;;
esac
`
	nsenter := filepath.Join(root, "nsenter")
	if err := os.WriteFile(nsenter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	state := rootfsstate.State{ContainerID: "container", FormatVersion: rootfsstate.FormatDelegationV1, RuntimeType: config.RunscRuntimeType, Phase: rootfsstate.PhaseDeleting, MountNamespace: stat.Ino, MergedDir: "/merged", DelegateBinary: "/opt/gvisor/runsc", DelegateArgs: []string{"--root=/run/runsc", "create", "container"}}
	return runscRecoveryFixture{proc: proc, ns: ns, nsenter: nsenter, log: log, state: state}
}

func TestRunscRecoveryClosesGoferBeforeUnmount(t *testing.T) {
	for _, tc := range []struct {
		name, action, host, order string
		failed                    bool
	}{
		{"normal", "true", "", "delete\nunmount\n", false},
		{"delete failure", "echo gofer-busy >&2; exit 1", "", "delete\n", true},
		{"namespace exits", "rm -rf 'NAMESPACE'", "", "delete\n", false},
		{"propagated host mount", "rm -rf 'NAMESPACE'", "1 0 0:1 / /merged rw - overlay overlay rw\n", "delete\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunscRecoveryFixture(t, tc.action, tc.host)
			f.state.DeleteArgs = []string{"--root=/run/runsc", "--debug", "delete", "container"}
			err := (NamespaceRecoveryCleaner{ProcRoot: f.proc, Nsenter: f.nsenter}).Cleanup(context.Background(), f.state)
			if (err != nil) != tc.failed {
				t.Fatalf("cleanup error: %v", err)
			}
			order, err := os.ReadFile(f.log)
			if err != nil || string(order) != tc.order {
				t.Fatalf("order=%q error=%v", order, err)
			}
			args, err := os.ReadFile(f.log + ".args")
			want := "-t\n123\n-m\n--\n/opt/gvisor/runsc\n--root=/run/runsc\n--debug\ndelete\n--force\ncontainer\n"
			if err != nil || string(args) != want {
				t.Fatalf("delegate arguments=%q error=%v", args, err)
			}
		})
	}
}

func TestRunscRecoveryMissingNamespaceFailsClosed(t *testing.T) {
	f := newRunscRecoveryFixture(t, "true", "")
	if err := os.RemoveAll(f.ns); err != nil {
		t.Fatal(err)
	}
	if err := (NamespaceRecoveryCleaner{ProcRoot: f.proc, Nsenter: f.nsenter}).Cleanup(context.Background(), f.state); err == nil {
		t.Fatal("unverified namespace fallback accepted")
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("delegate was invoked: %v", err)
	}
}
