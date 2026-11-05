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
	"strings"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func TestWrapperFlatCreateLifecycle(t *testing.T) {
	snapshotRoot := t.TempDir()
	source := sourceOverlayFixture(t, snapshotRoot, 2)
	source.Snapshotter = "overlayfs"
	source.SnapshotKey = "active-key"
	source.RootfsChainID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bundle := writeSpec(t, specs.Spec{
		Root:  &specs.Root{Path: "rootfs", Readonly: true},
		Linux: &specs.Linux{MountLabel: "system_u:object_r:container_file_t:s0"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:           "true",
			"io.kubernetes.cri.sandbox-uid":    "pod-uid-1",
			"io.kubernetes.cri.container-name": "dev",
		},
	})
	source.MountPoint = filepath.Join(bundle, "rootfs")
	mountInfoPath := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(mountInfoPath, []byte(overlayMountInfoLine(source.MountPoint, source)), 0o644); err != nil {
		t.Fatal(err)
	}

	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	rootfsClient := &flatLifecycleClient{
		resolveResponse: apiv1alpha1.ResolveRootfsResponse{
			Enabled:      true,
			UpperDir:     filepath.Join(t.TempDir(), "upper"),
			WorkDir:      filepath.Join(t.TempDir(), "work"),
			MetaDir:      filepath.Join(t.TempDir(), "meta"),
			LeaseID:      "lease-1",
			MountOptions: []string{"index=off", "metacopy=off"},
		},
	}
	for _, path := range []string{rootfsClient.resolveResponse.UpperDir, rootfsClient.resolveResponse.WorkDir, rootfsClient.resolveResponse.MetaDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	flatMounter := &recordingFlatMounter{}
	supervisor := &recordingDelegateSupervisor{delegate: rootfsstate.ProcessIdentity{PID: 303, StartTime: 404}}
	wrapper := Wrapper{
		Config: Config{
			DefaultDelegateRuntime:      "/usr/bin/runc",
			RuntimeRoot:                 runtimeRoot,
			OverlayMode:                 OverlayModeFlatLowerdirs,
			ContainerdNamespace:         "k8s.io",
			AllowedOverlaySnapshotRoots: []string{snapshotRoot},
		},
		Rootfs:             rootfsClient,
		SnapshotSource:     staticSnapshotSource{source: source},
		FlatMounter:        flatMounter,
		NormalUnmounter:    &recordingNormalUnmounter{},
		DelegateSupervisor: supervisor,
		ProcessIdentity: func() (rootfsstate.ProcessIdentity, error) {
			return rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}, nil
		},
		ExecutablePath: func() (string, error) {
			return "/usr/local/libexec/rootpv-flat-gpu/rootfs-runtime-wrapper", nil
		},
		MountInfoPath: mountInfoPath,
		SpecStore:     FileSpecStore{},
	}

	args := []string{"create", "--bundle", bundle, "container-id"}
	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", args, []string{"A=B"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rootfsClient.resolveRequest.Snapshotter != "overlayfs" || rootfsClient.resolveRequest.RootfsChainID != source.RootfsChainID {
		t.Fatalf("base identity not sent to rootfsd: %+v", rootfsClient.resolveRequest)
	}
	state := readState(t, runtimeRoot, "container-id")
	if !reflect.DeepEqual(flatMounter.lowerDirs, source.LowerDirs) || flatMounter.upper != rootfsClient.resolveResponse.UpperDir ||
		flatMounter.work != rootfsClient.resolveResponse.WorkDir || flatMounter.merged != state.MergedDir ||
		flatMounter.probed != rootfsClient.resolveResponse.UpperDir || !reflect.DeepEqual(flatMounter.resolverOptions, rootfsClient.resolveResponse.MountOptions) ||
		flatMounter.mountLabel != "system_u:object_r:container_file_t:s0" {
		t.Fatalf("unexpected flat mount: %+v", flatMounter)
	}
	if got, want := rootfsClient.calls, []string{"resolve", "mounted", "begin", "commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle calls=%v want=%v", got, want)
	}
	if supervisor.request.LeaseID != "lease-1" || supervisor.request.AttemptToken != "attempt-token" || supervisor.request.Delegate != "/usr/bin/runc" {
		t.Fatalf("unexpected delegate request: %+v", supervisor.request)
	}
	if state.FormatVersion != rootfsstate.FormatFlatV1 || state.Phase != rootfsstate.PhaseCreated || state.LeaseID != "lease-1" {
		t.Fatalf("unexpected final state: %+v", state)
	}
	if state.Wrapper != (rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}) || state.Delegate != supervisor.delegate {
		t.Fatalf("process identities missing from state: %+v", state)
	}
	if state.WrapperBinary != "/usr/local/libexec/rootpv-flat-gpu/rootfs-runtime-wrapper" || !reflect.DeepEqual(state.WrapperArgs, args) || state.DelegateBinary != "/usr/bin/runc" || !reflect.DeepEqual(state.DelegateArgs, args) {
		t.Fatalf("runtime invocation evidence missing from state: %+v", state)
	}
	if got := readSpec(t, bundle); got.Root.Path != state.MergedDir || got.Root.Readonly {
		t.Fatalf("spec root not rewritten: %+v", got.Root)
	}
}

func TestWrapperFlatRunIsRejected(t *testing.T) {
	bundle := writeSpec(t, specs.Spec{
		Root: &specs.Root{Path: "rootfs"},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:           "true",
			"io.kubernetes.cri.container-name": "dev",
		},
	})
	wrapper := Wrapper{
		Config:    Config{DefaultDelegateRuntime: "/usr/bin/runc", RuntimeRoot: t.TempDir(), OverlayMode: OverlayModeFlatLowerdirs},
		Executor:  &fakeExecutor{},
		SpecStore: FileSpecStore{},
	}
	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"run", "--bundle", bundle, "container-id"}, nil)
	if err == nil || !strings.Contains(err.Error(), "runtime_command_unsupported") {
		t.Fatalf("expected flat run rejection, got %v", err)
	}
}

func TestWrapperFlatCreateDelegateFailureRollsBack(t *testing.T) {
	snapshotRoot := t.TempDir()
	source := sourceOverlayFixture(t, snapshotRoot, 1)
	source.Snapshotter = "overlayfs"
	source.SnapshotKey = "active-key"
	source.RootfsChainID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	bundle := writeSpec(t, specs.Spec{
		Root: &specs.Root{Path: "rootfs", Readonly: true},
		Annotations: map[string]string{
			rootfs.AnnotationEnabled:           "true",
			"io.kubernetes.cri.sandbox-uid":    "pod-uid-1",
			"io.kubernetes.cri.container-name": "dev",
		},
	})
	originalRoot := filepath.Join(bundle, "rootfs")
	source.MountPoint = originalRoot
	mountInfoPath := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(mountInfoPath, []byte(overlayMountInfoLine(source.MountPoint, source)), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	client := &flatLifecycleClient{resolveResponse: apiv1alpha1.ResolveRootfsResponse{
		Enabled: true, UpperDir: filepath.Join(t.TempDir(), "upper"), WorkDir: filepath.Join(t.TempDir(), "work"), MetaDir: filepath.Join(t.TempDir(), "meta"), LeaseID: "lease-1",
	}}
	for _, path := range []string{client.resolveResponse.UpperDir, client.resolveResponse.WorkDir, client.resolveResponse.MetaDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unmounter := &recordingNormalUnmounter{}
	cleaner := &recordingRuntimeCleaner{}
	wrapper := Wrapper{
		Config:             Config{DefaultDelegateRuntime: "/usr/bin/runc", RuntimeRoot: runtimeRoot, OverlayMode: OverlayModeFlatLowerdirs, ContainerdNamespace: "k8s.io", AllowedOverlaySnapshotRoots: []string{snapshotRoot}},
		Rootfs:             client,
		SnapshotSource:     staticSnapshotSource{source: source},
		FlatMounter:        &recordingFlatMounter{},
		NormalUnmounter:    unmounter,
		UnmountVerifier:    &orderedUnmountVerifier{order: &[]string{}},
		DelegateSupervisor: &recordingDelegateSupervisor{delegate: rootfsstate.ProcessIdentity{PID: 303, StartTime: 404}, err: errors.New("runc create failed")},
		RuntimeCleaner:     cleaner,
		ProcessIdentity: func() (rootfsstate.ProcessIdentity, error) {
			return rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}, nil
		},
		MountInfoPath: mountInfoPath,
		SpecStore:     FileSpecStore{},
	}
	err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", bundle, "container-id"}, nil)
	if err == nil {
		t.Fatal("expected delegate failure")
	}
	if !cleaner.called || cleaner.containerID != "container-id" {
		t.Fatalf("partial runtime was not checked/cleaned: %+v", cleaner)
	}
	if len(unmounter.paths) != 1 {
		t.Fatalf("expected normal rollback unmount, got %v", unmounter.paths)
	}
	if got := readSpec(t, bundle); got.Root.Path != "rootfs" || !got.Root.Readonly {
		t.Fatalf("original spec was not restored: %+v", got.Root)
	}
	if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("runtime state should be removed after complete rollback: %v", err)
	}
	if got := client.calls[len(client.calls)-1]; got != "release" {
		t.Fatalf("lease release was not last lifecycle call: %v", client.calls)
	}
}

func TestWrapperFlatCreateRollsBackStateSaveFailures(t *testing.T) {
	for failAt := 1; ; failAt++ {
		if flatCreateStateSaveOccursAfterCommit(t, failAt) {
			return
		}
	}
}

func flatCreateStateSaveOccursAfterCommit(t *testing.T, failAt int) bool {
	t.Helper()
	setup := newFlatCreateRollbackSetup(t, flatCreateSideEffectCase{})
	setup.wrapper.RuntimeStateStore = &faultStateStore{failAt: failAt}
	if err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", setup.bundle, "container-id"}, nil); err == nil {
		t.Fatal("expected injected state save failure")
	}
	if flatLifecycleCallExists(setup.client.calls, "commit") {
		return true
	}
	if got := setup.client.calls[len(setup.client.calls)-1]; got != "release" {
		t.Fatalf("lease was not released: %v", setup.client.calls)
	}
	if _, err := os.Stat(rootfsstate.Path(setup.runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("state was not removed: %v", err)
	}
	if failAt >= 3 && len(setup.unmounter.paths) != 1 {
		t.Fatalf("mounted failure was not unmounted: %v", setup.unmounter.paths)
	}
	return false
}

func flatLifecycleCallExists(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestWrapperFlatCreateRollsBackSideEffectFailures(t *testing.T) {
	for _, test := range flatCreateSideEffectCases() {
		t.Run(test.name, func(t *testing.T) { assertFlatCreateSideEffectRollback(t, test) })
	}
}

type flatCreateSideEffectCase struct {
	name          string
	invalidMount  bool
	probeErr      error
	mountErr      error
	specSaveError bool
	markErr       error
	commitErr     error
	commitCreated *bool
	wantUnmount   bool
	wantCleaner   bool
}

func flatCreateSideEffectCases() []flatCreateSideEffectCase {
	return []flatCreateSideEffectCase{
		{name: "mountinfo", invalidMount: true},
		{name: "probe", probeErr: errors.New("probe failed")},
		{name: "mount", mountErr: errors.New("mount failed"), wantUnmount: true},
		{name: "spec-save", specSaveError: true, wantUnmount: true},
		{name: "mark-mounted", markErr: errors.New("mark failed"), wantUnmount: true},
		{name: "commit", commitErr: errors.New("commit failed"), wantUnmount: true, wantCleaner: true},
		{name: "commit-not-created", commitCreated: boolPointer(false), wantUnmount: true, wantCleaner: true},
	}
}

func assertFlatCreateSideEffectRollback(t *testing.T, test flatCreateSideEffectCase) {
	t.Helper()
	setup := newFlatCreateRollbackSetup(t, test)
	if err := setup.wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"create", "--bundle", setup.bundle, "container-id"}, nil); err == nil {
		t.Fatal("expected injected failure")
	}
	if got := setup.client.calls[len(setup.client.calls)-1]; got != "release" {
		t.Fatalf("lease was not released: %v", setup.client.calls)
	}
	if got := len(setup.unmounter.paths) > 0; got != test.wantUnmount {
		t.Fatalf("unmounted=%v want=%v paths=%v", got, test.wantUnmount, setup.unmounter.paths)
	}
	if setup.cleaner.called != test.wantCleaner {
		t.Fatalf("runtime cleaner called=%v want=%v", setup.cleaner.called, test.wantCleaner)
	}
	if _, err := os.Stat(rootfsstate.Path(setup.runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("state was not removed: %v", err)
	}
}

type flatCreateRollbackSetup struct {
	wrapper     Wrapper
	client      *flatLifecycleClient
	unmounter   *recordingNormalUnmounter
	cleaner     *recordingRuntimeCleaner
	bundle      string
	runtimeRoot string
}

func newFlatCreateRollbackSetup(t *testing.T, test flatCreateSideEffectCase) flatCreateRollbackSetup {
	t.Helper()
	snapshotRoot := t.TempDir()
	source := sourceOverlayFixture(t, snapshotRoot, 1)
	source.Snapshotter, source.SnapshotKey = "overlayfs", "active-key"
	source.RootfsChainID = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	bundle := writeSpec(t, flatCreateTestSpec())
	source.MountPoint = filepath.Join(bundle, "rootfs")
	mountInfo := overlayMountInfoLine(source.MountPoint, source)
	if test.invalidMount {
		mountInfo = "malformed mountinfo\n"
	}
	mountInfoPath := writeFlatMountInfo(t, mountInfo)
	client := newFlatLifecycleClient(t, test.markErr, test.commitErr, test.commitCreated)
	return newFlatCreateRollbackWrapper(t, flatCreateRollbackRequest{
		test: test, snapshotRoot: snapshotRoot, source: source,
		bundle: bundle, mountInfoPath: mountInfoPath, client: client,
	})
}

func flatCreateTestSpec() specs.Spec {
	return specs.Spec{Root: &specs.Root{Path: "rootfs", Readonly: true}, Annotations: map[string]string{
		rootfs.AnnotationEnabled: "true", "io.kubernetes.cri.sandbox-uid": "pod-uid-1", "io.kubernetes.cri.container-name": "dev",
	}}
}

func writeFlatMountInfo(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newFlatLifecycleClient(t *testing.T, markErr, commitErr error, commitCreated *bool) *flatLifecycleClient {
	t.Helper()
	client := &flatLifecycleClient{markErr: markErr, commitErr: commitErr, commitCreated: commitCreated, resolveResponse: apiv1alpha1.ResolveRootfsResponse{
		Enabled: true, UpperDir: filepath.Join(t.TempDir(), "upper"), WorkDir: filepath.Join(t.TempDir(), "work"), MetaDir: filepath.Join(t.TempDir(), "meta"), LeaseID: "lease-1",
	}}
	for _, path := range []string{client.resolveResponse.UpperDir, client.resolveResponse.WorkDir, client.resolveResponse.MetaDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

type flatCreateRollbackRequest struct {
	test          flatCreateSideEffectCase
	snapshotRoot  string
	source        SourceOverlay
	bundle        string
	mountInfoPath string
	client        *flatLifecycleClient
}

func newFlatCreateRollbackWrapper(t *testing.T, request flatCreateRollbackRequest) flatCreateRollbackSetup {
	t.Helper()
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	unmounter, cleaner := &recordingNormalUnmounter{}, &recordingRuntimeCleaner{}
	var specStore SpecStore = FileSpecStore{}
	if request.test.specSaveError {
		specStore = &faultSpecStore{failAt: 1}
	}
	wrapper := Wrapper{
		Config: Config{DefaultDelegateRuntime: "/usr/bin/runc", RuntimeRoot: runtimeRoot, OverlayMode: OverlayModeFlatLowerdirs, ContainerdNamespace: "k8s.io", AllowedOverlaySnapshotRoots: []string{request.snapshotRoot}},
		Rootfs: request.client, SnapshotSource: staticSnapshotSource{source: request.source}, FlatMounter: &recordingFlatMounter{probeErr: request.test.probeErr, mountErr: request.test.mountErr}, NormalUnmounter: unmounter,
		UnmountVerifier: &orderedUnmountVerifier{order: &[]string{}}, DelegateSupervisor: &recordingDelegateSupervisor{delegate: rootfsstate.ProcessIdentity{PID: 303, StartTime: 404}}, RuntimeCleaner: cleaner,
		ProcessIdentity: func() (rootfsstate.ProcessIdentity, error) {
			return rootfsstate.ProcessIdentity{PID: 101, StartTime: 202}, nil
		},
		MountInfoPath: request.mountInfoPath, SpecStore: specStore,
	}
	return flatCreateRollbackSetup{wrapper: wrapper, client: request.client, unmounter: unmounter, cleaner: cleaner, bundle: request.bundle, runtimeRoot: runtimeRoot}
}

func TestWrapperFlatDeleteOrder(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	state := flatDeleteState(runtimeRoot)
	writeState(t, runtimeRoot, state.ContainerID, state)
	var order []string
	client := &orderedDeleteClient{order: &order}
	wrapper := Wrapper{
		Config:          Config{DefaultDelegateRuntime: "/usr/bin/runc", RuntimeRoot: runtimeRoot, OverlayMode: OverlayModeFlatLowerdirs},
		Rootfs:          client,
		NormalUnmounter: &orderedNormalUnmounter{order: &order},
		UnmountVerifier: &orderedUnmountVerifier{order: &order},
		CommandRunner:   &orderedCommandRunner{order: &order},
	}
	if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"delete", "container-id"}, []string{"A=B"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"unmount", "verify", "delegate", "release"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order=%v want=%v", order, want)
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, "container-id")); !os.IsNotExist(err) {
		t.Fatalf("runtime state should be removed, stat err=%v", err)
	}
}

func TestWrapperFlatDeleteRetainsStateOnFailure(t *testing.T) {
	tests := []struct {
		name        string
		unmountErr  error
		verifyErr   error
		delegateErr error
		releaseErr  error
		wantOrder   []string
	}{
		{name: "unmount", unmountErr: errors.New("busy"), wantOrder: []string{"unmount"}},
		{name: "verify", verifyErr: errors.New("still mounted"), wantOrder: []string{"unmount", "verify"}},
		{name: "delegate", delegateErr: errors.New("delete failed"), wantOrder: []string{"unmount", "verify", "delegate"}},
		{name: "release", releaseErr: errors.New("rootfsd down"), wantOrder: []string{"unmount", "verify", "delegate", "release"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeRoot := filepath.Join(t.TempDir(), "runtime")
			state := flatDeleteState(runtimeRoot)
			writeState(t, runtimeRoot, state.ContainerID, state)
			var order []string
			wrapper := Wrapper{
				Config:          Config{DefaultDelegateRuntime: "/usr/bin/runc", RuntimeRoot: runtimeRoot, OverlayMode: OverlayModeFlatLowerdirs},
				Rootfs:          &orderedDeleteClient{order: &order, err: test.releaseErr},
				NormalUnmounter: &orderedNormalUnmounter{order: &order, err: test.unmountErr},
				UnmountVerifier: &orderedUnmountVerifier{order: &order, err: test.verifyErr},
				CommandRunner:   &orderedCommandRunner{order: &order, err: test.delegateErr},
			}
			if err := wrapper.Run(context.Background(), "rootfs-runtime-wrapper-runc", []string{"delete", "container-id"}, nil); err == nil {
				t.Fatal("expected fail-closed delete error")
			}
			if !reflect.DeepEqual(order, test.wantOrder) {
				t.Fatalf("order=%v want=%v", order, test.wantOrder)
			}
			if _, err := os.Stat(rootfsstate.Path(runtimeRoot, "container-id")); err != nil {
				t.Fatalf("runtime state should be retained: %v", err)
			}
		})
	}
}

type staticSnapshotSource struct {
	source SourceOverlay
	err    error
}

func (s staticSnapshotSource) Resolve(context.Context, string) (SourceOverlay, error) {
	return s.source, s.err
}

type recordingFlatMounter struct {
	probed          string
	lowerDirs       []string
	upper           string
	work            string
	merged          string
	resolverOptions []string
	mountLabel      string
	probeErr        error
	mountErr        error
}

func (m *recordingFlatMounter) Probe(upper string) error {
	m.probed = upper
	return m.probeErr
}

func (m *recordingFlatMounter) MountFlat(req FlatOverlayMountRequest) error {
	m.lowerDirs = append([]string(nil), req.LowerDirs...)
	m.upper = req.UpperDir
	m.work = req.WorkDir
	m.merged = req.MergedDir
	m.resolverOptions = append([]string(nil), req.ResolverOptions...)
	m.mountLabel = req.MountLabel
	return m.mountErr
}

type recordingNormalUnmounter struct {
	paths []string
	err   error
}

func (u *recordingNormalUnmounter) UnmountNormal(path string) error {
	u.paths = append(u.paths, path)
	return u.err
}

type recordingDelegateSupervisor struct {
	request  DelegateRequest
	delegate rootfsstate.ProcessIdentity
	err      error
}

type recordingRuntimeCleaner struct {
	called      bool
	delegate    string
	containerID string
	err         error
}

type faultStateStore struct {
	failAt int
	saves  int
}

type faultSpecStore struct {
	failAt int
	saves  int
}

func (s *faultSpecStore) Load(bundle string) (*specs.Spec, error) {
	return FileSpecStore{}.Load(bundle)
}

func (s *faultSpecStore) Save(bundle string, spec *specs.Spec) error {
	s.saves++
	if s.saves == s.failAt {
		return errors.New("injected spec save failure")
	}
	return FileSpecStore{}.Save(bundle, spec)
}

func (s *faultStateStore) Load(runtimeRoot, containerID string) (rootfsstate.State, error) {
	return rootfsstate.Load(runtimeRoot, containerID)
}

func (s *faultStateStore) Save(runtimeRoot, containerID string, state rootfsstate.State) error {
	s.saves++
	if s.saves == s.failAt {
		return errors.New("injected state save failure")
	}
	return rootfsstate.Save(runtimeRoot, containerID, state)
}

func (s *faultStateStore) Remove(runtimeRoot, containerID string) error {
	return os.RemoveAll(filepath.Join(runtimeRoot, containerID))
}

func (c *recordingRuntimeCleaner) CleanupPartial(_ context.Context, delegate, containerID string, _, _ []string) error {
	c.called = true
	c.delegate = delegate
	c.containerID = containerID
	return c.err
}

func (s *recordingDelegateSupervisor) Run(_ context.Context, req DelegateRequest, registered func(rootfsstate.ProcessIdentity) error) error {
	s.request = req
	if err := registered(s.delegate); err != nil {
		return err
	}
	return s.err
}

type flatLifecycleClient struct {
	resolveRequest  apiv1alpha1.ResolveRootfsRequest
	resolveResponse apiv1alpha1.ResolveRootfsResponse
	calls           []string
	markErr         error
	commitErr       error
	commitCreated   *bool
}

func flatDeleteState(runtimeRoot string) rootfsstate.State {
	return rootfsstate.State{
		FormatVersion:  rootfsstate.FormatFlatV1,
		ContainerID:    "container-id",
		LeaseID:        "lease-1",
		Phase:          rootfsstate.PhaseCreated,
		Snapshotter:    "overlayfs",
		RootfsChainID:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MergedDir:      filepath.Join(runtimeRoot, "container-id", "merged"),
		DelegateBinary: "/usr/bin/runc",
		DelegateArgs:   []string{"create", "container-id"},
	}
}

type orderedNormalUnmounter struct {
	order *[]string
	err   error
}

func (u *orderedNormalUnmounter) UnmountNormal(string) error {
	*u.order = append(*u.order, "unmount")
	return u.err
}

type orderedUnmountVerifier struct {
	order *[]string
	err   error
}

func (v *orderedUnmountVerifier) ConfirmUnmounted(string) error {
	*v.order = append(*v.order, "verify")
	return v.err
}

type orderedCommandRunner struct {
	order *[]string
	err   error
}

func (r *orderedCommandRunner) Run(context.Context, string, []string, []string) error {
	*r.order = append(*r.order, "delegate")
	return r.err
}

type orderedDeleteClient struct {
	order          *[]string
	err            error
	releaseRequest apiv1alpha1.ReleaseRootfsRequest
}

func (c *orderedDeleteClient) ReleaseRootfs(_ context.Context, req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	*c.order = append(*c.order, "release")
	c.releaseRequest = req
	return apiv1alpha1.ReleaseRootfsResponse{Released: c.err == nil}, c.err
}
func (c *orderedDeleteClient) ResolveRootfs(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	return apiv1alpha1.ResolveRootfsResponse{}, nil
}
func (c *orderedDeleteClient) MarkMounted(context.Context, apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	return apiv1alpha1.MarkMountedResponse{}, nil
}
func (c *orderedDeleteClient) BeginCreate(context.Context, apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	return apiv1alpha1.BeginCreateResponse{}, nil
}
func (c *orderedDeleteClient) RegisterDelegate(context.Context, apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	return apiv1alpha1.RegisterDelegateResponse{}, nil
}
func (c *orderedDeleteClient) CommitCreate(context.Context, apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	return apiv1alpha1.CommitCreateResponse{}, nil
}

func (c *flatLifecycleClient) ResolveRootfs(_ context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	c.resolveRequest = req
	c.calls = append(c.calls, "resolve")
	return c.resolveResponse, nil
}

func (c *flatLifecycleClient) MarkMounted(_ context.Context, req apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	c.calls = append(c.calls, "mounted")
	if req.LeaseID != c.resolveResponse.LeaseID {
		return apiv1alpha1.MarkMountedResponse{}, os.ErrInvalid
	}
	if c.markErr != nil {
		return apiv1alpha1.MarkMountedResponse{}, c.markErr
	}
	return apiv1alpha1.MarkMountedResponse{Mounted: true}, nil
}

func (c *flatLifecycleClient) BeginCreate(_ context.Context, req apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	c.calls = append(c.calls, "begin")
	if req.WrapperPID != 101 || req.WrapperStartTime != 202 {
		return apiv1alpha1.BeginCreateResponse{}, os.ErrInvalid
	}
	return apiv1alpha1.BeginCreateResponse{AttemptToken: "attempt-token"}, nil
}

func (c *flatLifecycleClient) RegisterDelegate(context.Context, apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	return apiv1alpha1.RegisterDelegateResponse{Registered: true}, nil
}

func (c *flatLifecycleClient) CommitCreate(_ context.Context, req apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	c.calls = append(c.calls, "commit")
	if req.AttemptToken != "attempt-token" {
		return apiv1alpha1.CommitCreateResponse{}, os.ErrInvalid
	}
	created := true
	if c.commitCreated != nil {
		created = *c.commitCreated
	}
	return apiv1alpha1.CommitCreateResponse{Created: created}, c.commitErr
}

func boolPointer(value bool) *bool {
	return &value
}

func (c *flatLifecycleClient) ReleaseRootfs(context.Context, apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	c.calls = append(c.calls, "release")
	return apiv1alpha1.ReleaseRootfsResponse{Released: true}, nil
}
