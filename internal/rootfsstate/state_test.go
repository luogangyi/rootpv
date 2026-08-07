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

package rootfsstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFlatRuntimeStateAtomicRoundTrip(t *testing.T) {
	root := t.TempDir()
	state := State{
		FormatVersion:    FormatFlatV1,
		ContainerID:      "container-1",
		LeaseID:          "lease-1",
		Phase:            PhaseCreatingRegistered,
		AttemptStatus:    AttemptRegistered,
		AttemptToken:     "attempt-1",
		RuntimeNamespace: "k8s.io",
		Snapshotter:      "overlayfs",
		SnapshotKey:      "container-1",
		RootfsChainID:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OriginalRootPath: "/run/containerd/task/rootfs",
		MergedDir:        "/run/rootpv/rootfs/container-1/merged",
		UpperDir:         "/pvc/rootfs/upper",
		WorkDir:          "/pvc/rootfs/work",
		MetaDir:          "/pvc/rootfs/meta",
		SourceMountPoint: "/run/containerd/task/rootfs",
		LowerDirCount:    3,
		Wrapper:          ProcessIdentity{PID: 100, StartTime: 1000},
		Delegate:         ProcessIdentity{PID: 101, StartTime: 1001},
		Shim:             ProcessIdentity{PID: 99, StartTime: 999},
		MountNamespace:   4026533000,
		WrapperBinary:    "/usr/local/libexec/rootpv-flat-gpu/rootfs-runtime-wrapper",
		WrapperArgs:      []string{"--root", "/run/containerd/runc/k8s.io", "create", "container-1"},
		DelegateBinary:   "/usr/bin/runc",
		DelegateArgs:     []string{"--root", "/run/containerd/runc/k8s.io"},
		CreatedAt:        time.Unix(100, 0).UTC(),
		UpdatedAt:        time.Unix(101, 0).UTC(),
	}
	if err := Save(root, state.ContainerID, state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(root, state.ContainerID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.FormatVersion != FormatFlatV1 || got.Phase != PhaseCreatingRegistered || got.AttemptToken != "attempt-1" || got.Delegate.PID != 101 || got.MountNamespace != state.MountNamespace || got.WrapperBinary != state.WrapperBinary || len(got.WrapperArgs) != 4 {
		t.Fatalf("unexpected round trip: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, state.ContainerID, "state.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("atomic temp file remains: %v", err)
	}
}

func TestSaveRefreshesUpdatedAt(t *testing.T) {
	root := t.TempDir()
	state := State{FormatVersion: FormatNestedV1, ContainerID: "container-1", MergedDir: "/run/rootpv/rootfs/container-1/merged"}
	if err := Save(root, state.ContainerID, state); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, err := Load(root, state.ContainerID)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	if err := Save(root, first.ContainerID, first); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	second, err := Load(root, state.ContainerID)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt was not refreshed: first=%s second=%s", first.UpdatedAt, second.UpdatedAt)
	}
}

func TestFlatRuntimeStateRejectsInvalidVersionAndPhaseFields(t *testing.T) {
	assertInvalidFlatStates(t, invalidVersionStateCases())
	assertInvalidFlatStates(t, invalidInvocationStateCases())
	assertInvalidFlatStates(t, invalidLifecycleStateCases())
}

type invalidFlatStateCase struct {
	name   string
	mutate func(*State)
	want   string
}

func invalidVersionStateCases() []invalidFlatStateCase {
	return []invalidFlatStateCase{{name: "unknown version", want: "unsupported runtime state format"}}
}

func invalidInvocationStateCases() []invalidFlatStateCase {
	return []invalidFlatStateCase{
		{
			name: "wrapper args without binary",
			mutate: func(s *State) {
				s.FormatVersion = FormatFlatV1
				s.WrapperArgs = []string{"create", "container-1"}
				s.Wrapper = ProcessIdentity{PID: 100, StartTime: 1000}
			},
			want: "wrapper binary and args must be recorded together",
		},
		{name: "missing lease", mutate: func(s *State) { s.FormatVersion = FormatFlatV1; s.LeaseID = "" }, want: "lease ID is required"},
	}
}

func invalidLifecycleStateCases() []invalidFlatStateCase {
	return []invalidFlatStateCase{
		{
			name: "registered without delegate",
			mutate: func(s *State) {
				s.FormatVersion = FormatFlatV1
				s.Phase = PhaseCreatingRegistered
				s.AttemptStatus = AttemptRegistered
				s.AttemptToken = "attempt-1"
				s.Wrapper = ProcessIdentity{PID: 100, StartTime: 1000}
			},
			want: "delegate process identity is required",
		},
		{
			name: "pending without wrapper",
			mutate: func(s *State) {
				s.FormatVersion = FormatFlatV1
				s.Phase = PhaseCreatingPending
				s.AttemptStatus = AttemptPending
				s.AttemptToken = "attempt-1"
			},
			want: "wrapper process identity is required",
		},
	}
}

func assertInvalidFlatStates(t *testing.T, tests []invalidFlatStateCase) {
	t.Helper()
	base := State{FormatVersion: "unknown", ContainerID: "container-1", LeaseID: "lease-1", Phase: PhaseAcquired, Snapshotter: "overlayfs", RootfsChainID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MergedDir: "/run/rootpv/rootfs/container-1/merged"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := base
			if tt.mutate != nil {
				tt.mutate(&state)
			}
			if err := Validate(state); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
	}
}

func TestOlderFlatRuntimeStateWithoutWrapperInvocationRemainsReadable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "container-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"format_version":"flat-overlay-runtime-v1","container_id":"container-1","lease_id":"lease-1","phase":"created","snapshotter":"overlayfs","rootfs_chain_id":"sha256:old","merged_dir":"/run/merged"}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, "container-1")
	if err != nil {
		t.Fatalf("Load older flat state: %v", err)
	}
	if got.WrapperBinary != "" || got.WrapperArgs != nil {
		t.Fatalf("unexpected invocation fields: %+v", got)
	}
}

func TestLoadRejectsMalformedRuntimeState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "container-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "container-1"); err == nil || !strings.Contains(err.Error(), "decode runtime state") {
		t.Fatalf("expected malformed state error, got %v", err)
	}
}

func TestLegacyRuntimeStateRemainsReadable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "container-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"container_id":"container-1","merged_dir":"/run/legacy/merged"}`)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, "container-1")
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if got.FormatVersion != FormatNestedV1 || got.ContainerID != "container-1" {
		t.Fatalf("unexpected legacy state: %+v", got)
	}
}

func TestDelegationStateAllowsLightweightSidecar(t *testing.T) {
	state := State{
		FormatVersion:        FormatDelegationV1,
		ContainerID:          "sidecar-id",
		RuntimeAdapterID:     "runc-v1",
		RuntimeAdapterMode:   "Explicit",
		RootfsHandler:        "persistent-rootfs-runc",
		RuntimeType:          "io.containerd.runc.v2",
		DelegateBinary:       "/usr/bin/runc",
		ClusterAdapterDigest: "sha256:cluster",
		NodeResolvedDigest:   "sha256:node",
		RootfsEnabled:        false,
		Phase:                PhaseCreated,
	}
	if err := Validate(state); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
