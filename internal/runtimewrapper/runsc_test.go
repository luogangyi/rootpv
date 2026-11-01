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
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"gopkg.in/yaml.v3"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func TestRunscInvocationArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--overlay2=none", "create", "--bundle", "/bundle", "--user-log", "/user.log", "container"},
		{"--overlay2=none", "exec", "--process", "/process", "--internal-pid-file", "/pid", "container"},
		{"--overlay2=none", "wait", "--pid", "7", "--rootpid", "8", "container"},
		{"--overlay2=none", "events", "--stats", "container"},
		{"--overlay2=none", "events", "--interval", "2s", "container"},
		{"--overlay2=none", "ps", "--format", "json", "container"},
	} {
		inv, err := ParseInvocation(args)
		if err != nil || !inv.Handled || inv.ContainerID != "container" {
			t.Fatalf("args=%v: %#v %v", args, inv, err)
		}
	}
}

func TestRunscCreateRejectsHiddenWriteLayers(t *testing.T) {
	for _, tc := range []struct {
		name, mode  string
		args        []string
		annotations map[string]string
		valid       bool
	}{
		{name: "write-through", mode: OverlayModeFlatLowerdirs, args: []string{"--overlay2=none", "create", "container"}, valid: true},
		{name: "single dash", mode: OverlayModeFlatLowerdirs, args: []string{"-overlay2=none", "create", "container"}, valid: true},
		{name: "relative root", mode: OverlayModeFlatLowerdirs, args: []string{"--root=relative", "--overlay2=none", "create", "container"}},
		{name: "separate flag", mode: OverlayModeFlatLowerdirs, args: []string{"--overlay2", "none", "create", "container"}, valid: true},
		{name: "default overlay", mode: OverlayModeFlatLowerdirs, args: []string{"create", "container"}},
		{name: "overridden overlay", mode: OverlayModeFlatLowerdirs, args: []string{"--overlay2=none", "--overlay2=root:self", "create", "container"}},
		{name: "nested mode", mode: OverlayModeNestedRootfs, args: []string{"--overlay2=none", "create", "container"}},
		{name: "annotation override", mode: OverlayModeFlatLowerdirs, args: []string{"--overlay2=none", "create", "container"}, annotations: map[string]string{"dev.gvisor.spec.rootfs.overlay": "memory"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunscCreate(Config{OverlayMode: tc.mode}, &specs.Spec{Annotations: tc.annotations}, append([]string{"--root=/run/runsc"}, tc.args...))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

type orderedRunscCleaner struct {
	order *[]string
	args  []string
	err   error
}

func (c *orderedRunscCleaner) CleanupPartial(_ context.Context, _, _ string, args, _ []string) error {
	*c.order = append(*c.order, "runsc-delete")
	c.args = append([]string(nil), args...)
	return c.err
}

func TestRunscDeleteClosesGoferBeforeUnmount(t *testing.T) {
	for _, failure := range []bool{false, true} {
		runtimeRoot := t.TempDir()
		state := mergeDelegationState(flatDeleteState(runtimeRoot), rootfsstate.State{
			RuntimeType: config.RunscRuntimeType, DelegateBinary: "/opt/gvisor/runsc", RootfsEnabled: true,
			RuntimeAdapterID: "runsc-v1", RuntimeAdapterMode: "Explicit", RootfsHandler: "persistent-rootfs-runsc-v1",
			ClusterAdapterDigest: "cluster", NodeResolvedDigest: "node",
		})
		state.DelegateArgs = []string{"--root=/run/runsc", "create", "--bundle", "/bundle", state.ContainerID}
		writeState(t, runtimeRoot, state.ContainerID, state)
		var order []string
		var deleteErr error
		if failure {
			deleteErr = errors.New("gofer still alive")
		}
		cleaner := &orderedRunscCleaner{order: &order, err: deleteErr}
		wrapper := Wrapper{Config: Config{RuntimeAdapterCutover: true, RuntimeRoot: runtimeRoot, OverlayMode: OverlayModeFlatLowerdirs}, Rootfs: &orderedDeleteClient{order: &order}, NormalUnmounter: &orderedNormalUnmounter{order: &order}, UnmountVerifier: &orderedUnmountVerifier{order: &order}, CommandRunner: &orderedCommandRunner{order: &order}, RuntimeCleaner: cleaner}
		err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runsc-v1", []string{"delete", state.ContainerID}, nil)
		if !reflect.DeepEqual(cleaner.args, []string{"--root=/run/runsc", "delete", state.ContainerID}) {
			t.Fatalf("cleanup arguments: %v", cleaner.args)
		}
		if failure {
			if err == nil || !reflect.DeepEqual(order, []string{"runsc-delete"}) {
				t.Fatalf("unsafe rollback: %v %v", order, err)
			}
			if _, err := rootfsstate.Load(runtimeRoot, state.ContainerID); err != nil {
				t.Fatalf("recovery state lost: %v", err)
			}
		} else {
			if err != nil || !reflect.DeepEqual(order, []string{"runsc-delete", "unmount", "verify", "release"}) {
				t.Fatalf("delete order: %v %v", order, err)
			}
			if _, err := os.Stat(filepath.Join(runtimeRoot, state.ContainerID)); !os.IsNotExist(err) {
				t.Fatalf("state remains: %v", err)
			}
		}
	}
}

func runscCreateFixture(t *testing.T) (flatCreateRollbackSetup, string, string) {
	t.Helper()
	setup := newFlatCreateRollbackSetup(t, flatCreateSideEffectCase{})
	registry := filepath.Join(t.TempDir(), "registry.yaml")
	delegate := filepath.Join(t.TempDir(), "runsc")
	if err := os.WriteFile(delegate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	delegate, err := filepath.EvalSymlinks(delegate)
	if err != nil {
		t.Fatal(err)
	}
	entry := ResolvedRuntimeAdapter{SourceHandler: "runsc", RootfsHandler: "persistent-rootfs-runsc-v1", RuntimeType: config.RunscRuntimeType, DelegateBinary: delegate, ClusterDigest: "cluster", NodeResolvedDigest: "node"}
	data, err := yaml.Marshal(AdapterRegistry{FormatVersion: config.RuntimeAdapterRegistryV2, Adapters: map[string]ResolvedRuntimeAdapter{"runsc-v1": entry}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, data, 0o600); err != nil {
		t.Fatal(err)
	}
	spec := readSpec(t, setup.bundle)
	spec.Annotations[rootfs.AnnotationRuntimeAdapterID] = "runsc-v1"
	spec.Annotations[rootfs.AnnotationRuntimeAdapterMode] = "Explicit"
	spec.Annotations[rootfs.AnnotationRuntimeAdapterConfigHash] = "cluster"
	if err := SaveSpec(setup.bundle, &spec); err != nil {
		t.Fatal(err)
	}
	setup.wrapper.Config.RuntimeAdapterCutover, setup.wrapper.Config.RuntimeAdapterRegistry = true, registry
	return setup, registry, delegate
}

func TestRunscCutoverCreateAndWaitPinRealDelegate(t *testing.T) {
	setup, registry, delegate := runscCreateFixture(t)
	executor := &fakeExecutor{}
	setup.wrapper.Executor = executor
	args := []string{"--root=/run/runsc", "--overlay2=none", "create", "--bundle", setup.bundle, "--user-log", "/log", "container-id"}
	if err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runsc-v1", args, nil); err != nil {
		t.Fatal(err)
	}
	state := readState(t, setup.runtimeRoot, "container-id")
	if state.RuntimeType != config.RunscRuntimeType || state.DelegateBinary != delegate || state.FormatVersion != rootfsstate.FormatDelegationV1 {
		t.Fatalf("state: %#v", state)
	}
	if got := readSpec(t, setup.bundle); got.Root.Path != state.MergedDir {
		t.Fatal("persistent rootfs not used")
	}
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	for _, query := range [][]string{{"wait", "container-id"}, {"events", "--stats", "container-id"}, {"ps", "--format", "json", "container-id"}} {
		if err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runsc-v1", append([]string{"--root=/run/runsc"}, query...), nil); err != nil {
			t.Fatal(err)
		}
		if executor.path != delegate {
			t.Fatalf("query rerouted: %#v", executor)
		}
	}
}

// Stop after the first flat state save, before rollback can erase the evidence.
type interruptedRunscStore struct{ FileRuntimeStateStore }

func (s interruptedRunscStore) Save(root, id string, state rootfsstate.State) error {
	if err := s.FileRuntimeStateStore.Save(root, id, state); err != nil {
		return err
	}
	if state.Phase == rootfsstate.PhaseAcquired {
		panic("interrupted create")
	}
	return nil
}

func TestRunscInterruptedCreateRetainsRouting(t *testing.T) {
	setup, registry, delegate := runscCreateFixture(t)
	setup.wrapper.RuntimeStateStore = interruptedRunscStore{}
	args := []string{"--root=/run/runsc", "--overlay2=none", "create", "--bundle", setup.bundle, "container-id"}
	func() {
		defer func() {
			if recover() != "interrupted create" {
				t.Fatal("create did not reach the interruption")
			}
		}()
		if err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runsc-v1", args, nil); err != nil {
			t.Fatal(err)
		}
	}()
	state := readState(t, setup.runtimeRoot, "container-id")
	if state.RuntimeType != config.RunscRuntimeType || state.DelegateBinary != delegate || !state.RootfsEnabled {
		t.Fatalf("routing evidence lost: %#v", state)
	}
	if !reflect.DeepEqual(state.DelegateArgs, args) || state.LeaseID == "" {
		t.Fatalf("recovery evidence lost: %#v", state)
	}
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	if state.FormatVersion != rootfsstate.FormatFlatV1 || state.Phase != rootfsstate.PhaseAcquired {
		t.Fatalf("interrupted format/phase changed: %s/%s", state.FormatVersion, state.Phase)
	}
	setup.wrapper.RuntimeStateStore = FileRuntimeStateStore{}
	if err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runsc-v1", []string{"delete", "container-id"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := setup.client.calls[len(setup.client.calls)-1]; got != "release" {
		t.Fatalf("lease not released: %v", setup.client.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(setup.runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("state remains: %v", err)
	}
}

func TestRunscDeleteRejectsConflictingRoots(t *testing.T) {
	state := rootfsstate.State{DelegateArgs: []string{"--root=/run/runsc", "create", "container"}}
	for _, args := range [][]string{{"delete", "container"}, {"--root=/other", "delete", "container"}, {"--root=relative", "delete", "container"}} {
		got, err := runscDeleteArgs(state, args)
		if len(args) == 2 {
			if err != nil || got[0] != "--root=/run/runsc" {
				t.Fatalf("saved root not used: %v %v", got, err)
			}
		} else {
			if err == nil {
				t.Fatalf("unsafe root accepted: %v", args)
			}
		}
	}
}

func TestRunscExplicitDeletePreservesForceFlag(t *testing.T) {
	args := []string{"--root=/run/runsc", "delete", "--force=false", "container"}
	if got := partialRuntimeDeleteArgs(args, "container"); !reflect.DeepEqual(got, args) {
		t.Fatalf("explicit force changed: %v", got)
	}
}
