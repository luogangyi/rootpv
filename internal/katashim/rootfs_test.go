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

package katashim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	task "github.com/containerd/containerd/api/runtime/task/v2"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	api "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
	"gitcode.com/openFuyao/rootpv/internal/runtimewrapper"
)

type fixture struct {
	lifecycle *RootfsLifecycle
	request   *task.CreateTaskRequest
	delegate  *fakeTask
	steps     []string
	fail      string
}
type fakeTask struct {
	task.TaskService
	f        *fixture
	received *task.CreateTaskRequest
}

func (d *fakeTask) Create(_ context.Context, r *task.CreateTaskRequest) (*task.CreateTaskResponse, error) {
	d.f.steps = append(d.f.steps, "create")
	d.received = r
	if d.f.fail == "create" || d.f.fail == "rollback-delete" {
		return nil, errors.New("create failed")
	}
	return &task.CreateTaskResponse{Pid: 42}, nil
}
func (d *fakeTask) Delete(_ context.Context, r *task.DeleteRequest) (*task.DeleteResponse, error) {
	d.f.steps = append(d.f.steps, "delete")
	if d.f.fail == "rollback-delete" {
		return nil, errors.New("delete failed")
	}
	return &task.DeleteResponse{Pid: 42}, nil
}

type fakeRootfs struct{ f *fixture }

func (r fakeRootfs) ResolveRootfs(context.Context, api.ResolveRootfsRequest) (api.ResolveRootfsResponse, error) {
	r.f.steps = append(r.f.steps, "resolve")
	root := filepath.Join(r.f.lifecycle.Config.RuntimeRoot, "pvc")
	return api.ResolveRootfsResponse{Enabled: true, LeaseID: "lease", UpperDir: root + "/upper", WorkDir: root + "/work", MetaDir: root + "/meta"}, nil
}
func (r fakeRootfs) MarkMounted(context.Context, api.MarkMountedRequest) (api.MarkMountedResponse, error) {
	if r.f.fail == "mark-mounted" {
		return api.MarkMountedResponse{}, errors.New("mark mounted failed")
	}
	return api.MarkMountedResponse{Mounted: true}, nil
}
func (r fakeRootfs) BeginCreate(context.Context, api.BeginCreateRequest) (api.BeginCreateResponse, error) {
	if r.f.fail == "begin" {
		return api.BeginCreateResponse{}, errors.New("begin failed")
	}
	return api.BeginCreateResponse{AttemptToken: "attempt"}, nil
}
func (r fakeRootfs) RegisterDelegate(context.Context, api.RegisterDelegateRequest) (api.RegisterDelegateResponse, error) {
	if r.f.fail == "register" {
		return api.RegisterDelegateResponse{}, errors.New("register failed")
	}
	r.f.steps = append(r.f.steps, "register")
	return api.RegisterDelegateResponse{Registered: true}, nil
}
func (r fakeRootfs) CommitCreate(context.Context, api.CommitCreateRequest) (api.CommitCreateResponse, error) {
	r.f.steps = append(r.f.steps, "commit")
	if r.f.fail == "commit" {
		return api.CommitCreateResponse{}, errors.New("commit failed")
	}
	return api.CommitCreateResponse{Created: true}, nil
}
func (r fakeRootfs) ReleaseRootfs(context.Context, api.ReleaseRootfsRequest) (api.ReleaseRootfsResponse, error) {
	r.f.steps = append(r.f.steps, "release")
	return api.ReleaseRootfsResponse{Released: true}, nil
}

type fakeMount struct{ f *fixture }

func (m fakeMount) Probe(string) error {
	if m.f.fail == "probe" {
		return errors.New("probe failed")
	}
	return nil
}
func (m fakeMount) MountFlat(runtimewrapper.FlatOverlayMountRequest) error {
	m.f.steps = append(m.f.steps, "mount")
	if m.f.fail == "mount" {
		return errors.New("mount failed")
	}
	return nil
}
func (m fakeMount) UnmountNormal(string) error {
	m.f.steps = append(m.f.steps, "unmount")
	if m.f.fail == "unmount" {
		return errors.New("unmount failed")
	}
	return nil
}
func (m fakeMount) ConfirmUnmounted(string) error {
	m.f.steps = append(m.f.steps, "verify")
	return nil
}

type fakeSnapshot struct{ source runtimewrapper.SourceOverlay }

func (s fakeSnapshot) Resolve(context.Context, string) (runtimewrapper.SourceOverlay, error) {
	return s.source, nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dirs := []string{root + "/snapshot/1/fs", root + "/snapshot/2/fs", root + "/snapshot/2/work", root + "/bundle"}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	adapter := runtimewrapper.ResolvedRuntimeAdapter{Mode: config.RuntimeAdapterModeExplicit, SourceHandler: "kata-qemu", RootfsHandler: "persistent-rootfs-kata-qemu-v2", RuntimeType: config.KataQemuRuntimeType, DelegateBinary: "/opt/kata/shim", ClusterAdapterDigest: "cluster", NodeResolvedDigest: "node"}
	cfg := runtimewrapper.DefaultConfig()
	cfg.RuntimeRoot = root + "/runtime"
	cfg.OverlayMode = runtimewrapper.OverlayModeFlatLowerdirs
	cfg.AllowedOverlaySnapshotRoots = []string{root + "/snapshot"}
	f := &fixture{request: &task.CreateTaskRequest{ID: "container", Bundle: root + "/bundle"}}
	f.lifecycle = &RootfsLifecycle{
		Config: cfg, AdapterID: "kata-qemu-v2", Adapter: adapter, DelegatePID: 7,
		Identity: func(pid int) (rootfsstate.ProcessIdentity, error) {
			return rootfsstate.ProcessIdentity{PID: pid, StartTime: 99}, nil
		},
		MountNamespace: func() (uint64, error) { return 1, nil },
		Rootfs:         fakeRootfs{f},
		Mounter:        fakeMount{f},
		Unmounter:      fakeMount{f},
		Verifier:       fakeMount{f},
		Snapshot:       fakeSnapshot{runtimewrapper.SourceOverlay{Snapshotter: "overlayfs", SnapshotKey: "snapshot", RootfsChainID: "chain", LowerDirs: dirs[:1], UpperDir: dirs[1], WorkDir: dirs[2]}},
	}
	f.delegate = &fakeTask{f: f}
	spec := &specs.Spec{Root: &specs.Root{Path: "rootfs"}, Annotations: map[string]string{rootfs.AnnotationEnabled: "true", rootfs.AnnotationRuntimeAdapterID: "kata-qemu-v2", rootfs.AnnotationRuntimeAdapterMode: "Explicit", rootfs.AnnotationRuntimeAdapterConfigHash: "cluster"}}
	if err := runtimewrapper.SaveSpec(f.request.Bundle, spec); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPersistentCreateAndDeleteOrder(t *testing.T) {
	f := newFixture(t)
	if _, err := f.lifecycle.Create(context.Background(), f.delegate, f.request); err != nil {
		t.Fatal(err)
	}
	state, err := rootfsstate.Load(f.lifecycle.Config.RuntimeRoot, "container")
	if err != nil {
		t.Fatal(err)
	}
	if state.FormatVersion != rootfsstate.FormatShimV1 || state.Phase != rootfsstate.PhaseCreated {
		t.Fatalf("state: %#v", state)
	}
	if len(f.delegate.received.Rootfs) != 1 || f.delegate.received.Rootfs[0].Source != state.MergedDir || f.delegate.received.Rootfs[0].Type != "bind" {
		t.Fatalf("mounts: %#v", f.delegate.received.Rootfs)
	}
	if len(f.request.Rootfs) != 0 {
		t.Fatal("mutated caller request")
	}
	f.steps = nil
	if _, err := f.lifecycle.Delete(context.Background(), f.delegate, &task.DeleteRequest{ID: "container"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.steps, []string{"delete", "unmount", "verify", "release"}) {
		t.Fatalf("unsafe cleanup order: %v", f.steps)
	}
	if _, err := rootfsstate.Load(f.lifecycle.Config.RuntimeRoot, "container"); !os.IsNotExist(err) {
		t.Fatalf("state remained: %v", err)
	}
}

func TestCreateRollbackRetainsLeaseUntilDelegateAndMountAreClean(t *testing.T) {
	for _, failure := range []string{"probe", "mount", "mark-mounted", "begin", "register", "create", "commit", "rollback-delete"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			f.fail = failure
			if _, err := f.lifecycle.Create(context.Background(), f.delegate, f.request); err == nil {
				t.Fatal("expected failure")
			}
			released := strings.Contains(strings.Join(f.steps, ","), "release")
			if released == (failure == "rollback-delete") {
				t.Fatalf("unsafe lease release: %v", f.steps)
			}
			if failure == "rollback-delete" {
				if _, err := rootfsstate.Load(f.lifecycle.Config.RuntimeRoot, "container"); err != nil {
					t.Fatalf("recovery state lost: %v", err)
				}
			}
		})
	}
}

func TestDeleteUnmountFailureRetainsStateAndLease(t *testing.T) {
	f := newFixture(t)
	if _, err := f.lifecycle.Create(context.Background(), f.delegate, f.request); err != nil {
		t.Fatal(err)
	}
	f.steps = nil
	f.fail = "unmount"
	if _, err := f.lifecycle.Delete(context.Background(), f.delegate, &task.DeleteRequest{ID: "container"}); err == nil {
		t.Fatal("expected failure")
	}
	if !reflect.DeepEqual(f.steps, []string{"delete", "unmount"}) {
		t.Fatalf("lease released before unmount: %v", f.steps)
	}
	state, err := rootfsstate.Load(f.lifecycle.Config.RuntimeRoot, "container")
	if err != nil || state.Phase != rootfsstate.PhaseDeleting {
		t.Fatalf("recovery state: %#v %v", state, err)
	}
}

func TestAdapterMismatchFailsBeforeRootfsResolve(t *testing.T) {
	f := newFixture(t)
	spec, _ := runtimewrapper.LoadSpec(f.request.Bundle)
	spec.Annotations[rootfs.AnnotationRuntimeAdapterConfigHash] = "wrong"
	_ = runtimewrapper.SaveSpec(f.request.Bundle, spec)
	if _, err := f.lifecycle.Create(context.Background(), f.delegate, f.request); err == nil {
		t.Fatal("expected mismatch")
	}
	if len(f.steps) != 0 {
		t.Fatalf("touched resources: %v", f.steps)
	}
}

func TestSandboxAndUntargetedSidecarPassThrough(t *testing.T) {
	for _, sandbox := range []bool{true, false} {
		f := newFixture(t)
		spec, _ := runtimewrapper.LoadSpec(f.request.Bundle)
		if sandbox {
			spec.Annotations = map[string]string{"io.kubernetes.cri.container-type": "sandbox"}
		} else {
			spec.Annotations[rootfs.AnnotationContainer] = "app"
			spec.Annotations["io.kubernetes.cri.container-name"] = "sidecar"
		}
		_ = runtimewrapper.SaveSpec(f.request.Bundle, spec)
		if _, err := f.lifecycle.Create(context.Background(), f.delegate, f.request); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.steps, []string{"create"}) {
			t.Fatalf("ordinary rootfs changed: %v", f.steps)
		}
	}
}
