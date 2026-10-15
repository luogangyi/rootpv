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
	"fmt"
	"os"
	"path/filepath"
	"sync"

	task "github.com/containerd/containerd/api/runtime/task/v2"
	types "github.com/containerd/containerd/api/types"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/protobuf/proto"

	api "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
	"gitcode.com/openFuyao/rootpv/internal/runtimewrapper"
)

type RootfsLifecycle struct {
	Config         runtimewrapper.Config
	AdapterID      string
	Adapter        runtimewrapper.ResolvedRuntimeAdapter
	DelegatePID    int
	Rootfs         runtimewrapper.RootfsClient
	Snapshot       runtimewrapper.SnapshotSource
	Mounter        runtimewrapper.FlatOverlayMounter
	Unmounter      runtimewrapper.NormalUnmounter
	Verifier       runtimewrapper.UnmountVerifier
	Identity       func(int) (rootfsstate.ProcessIdentity, error)
	MountNamespace func() (uint64, error)
	mu             sync.Mutex
}

func NewRootfsLifecycle(cfg runtimewrapper.Config, id string, adapter runtimewrapper.ResolvedRuntimeAdapter, delegatePID int) *RootfsLifecycle {
	return &RootfsLifecycle{Config: cfg, AdapterID: id, Adapter: adapter, DelegatePID: delegatePID,
		Rootfs:   runtimewrapper.NewUnixResolver(cfg.RootfsdSocket),
		Snapshot: runtimewrapper.LazyContainerdSnapshotSource{Address: cfg.ContainerdAddress, Namespace: cfg.ContainerdNamespace},
		Mounter:  &runtimewrapper.SyscallFlatOverlayMounter{}, Unmounter: runtimewrapper.SyscallNormalUnmounter{}, Verifier: runtimewrapper.ProcUnmountVerifier{},
		Identity: runtimewrapper.ProcessIdentityFor, MountNamespace: runtimewrapper.CurrentMountNamespaceInode,
	}
}

func (l *RootfsLifecycle) validateAdapter(spec *specs.Spec) error {
	if !config.IsKataQemuRuntimeType(l.Adapter.RuntimeType) {
		return fmt.Errorf("Kata QEMU adapter required")
	}
	a := spec.Annotations
	if runtimewrapper.IsSandbox(spec) && a[rootfs.AnnotationRuntimeAdapterID] == "" {
		return nil
	}
	if a[rootfs.AnnotationRuntimeAdapterID] != l.AdapterID || a[rootfs.AnnotationRuntimeAdapterMode] != string(config.RuntimeAdapterModeExplicit) || a[rootfs.AnnotationRuntimeAdapterConfigHash] != l.Adapter.ClusterAdapterDigest {
		return fmt.Errorf("runtime_adapter_config_mismatch")
	}
	if handler := a["io.kubernetes.cri.sandbox-runtime-handler"]; handler != "" && handler != l.Adapter.RootfsHandler {
		return fmt.Errorf("runtime_adapter_state_mismatch")
	}
	return nil
}

func (l *RootfsLifecycle) Create(ctx context.Context, delegate task.TaskService, req *task.CreateTaskRequest) (*task.CreateTaskResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	spec, err := runtimewrapper.LoadSpec(req.Bundle)
	if err != nil {
		return nil, err
	}
	if err := l.validateAdapter(spec); err != nil {
		return nil, err
	}
	if !runtimewrapper.ShouldMountPersistentRootfs(spec) {
		return delegate.Create(ctx, req)
	}
	create, err := l.prepareCreate(ctx, spec, req)
	if err != nil {
		return nil, err
	}
	created, err := create.run(ctx, delegate, req)
	if err != nil {
		return nil, create.rollback(ctx, delegate, err)
	}
	return created, nil
}

// rootfsCreate tracks which resources must be rolled back if a create fails.
type rootfsCreate struct {
	lifecycle *RootfsLifecycle
	state     rootfsstate.State
	source    runtimewrapper.SourceOverlay
	resolve   api.ResolveRootfsRequest
	response  api.ResolveRootfsResponse
	mounted   bool
	called    bool
}

func (l *RootfsLifecycle) prepareCreate(ctx context.Context, spec *specs.Spec, req *task.CreateTaskRequest) (*rootfsCreate, error) {
	if req.Checkpoint != "" || l.Config.OverlayMode != runtimewrapper.OverlayModeFlatLowerdirs {
		return nil, fmt.Errorf("Kata RootPV requires flat-lowerdirs and a fresh create")
	}
	if _, err := rootfsstate.Load(l.Config.RuntimeRoot, req.ID); err == nil {
		return nil, fmt.Errorf("Kata RootPV state already exists for %s", req.ID)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	source, err := l.Snapshot.Resolve(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if err := runtimewrapper.ValidateSourceOverlay(source, l.Config.AllowedOverlaySnapshotRoots); err != nil {
		return nil, err
	}
	resolve, err := runtimewrapper.BuildResolveRequest(spec, runtimewrapper.Invocation{ContainerID: req.ID, Bundle: req.Bundle})
	if err != nil {
		return nil, err
	}
	resolve.Snapshotter, resolve.SnapshotKey, resolve.RootfsChainID = source.Snapshotter, source.SnapshotKey, source.RootfsChainID
	response, err := l.Rootfs.ResolveRootfs(ctx, resolve)
	if err != nil {
		return nil, err
	}
	if !response.Enabled || response.LeaseID == "" {
		return nil, fmt.Errorf("Kata rootfs resolve returned no lease: %s", response.Reason)
	}
	state := rootfsstate.State{FormatVersion: rootfsstate.FormatShimV1, ContainerID: req.ID, Namespace: resolve.Namespace, PodName: resolve.PodName, PodUID: resolve.PodUID, ContainerName: resolve.ContainerName,
		LeaseID: response.LeaseID, Phase: rootfsstate.PhaseAcquired, RuntimeNamespace: l.Config.ContainerdNamespace,
		Snapshotter: source.Snapshotter, SnapshotKey: source.SnapshotKey, RootfsChainID: source.RootfsChainID, Bundle: req.Bundle, OriginalRootPath: resolve.OriginalRootPath,
		UpperDir: response.UpperDir, WorkDir: response.WorkDir, MetaDir: response.MetaDir, MergedDir: filepath.Join(l.Config.RuntimeRoot, req.ID, "merged"), LowerDirCount: len(source.LowerDirs),
		RuntimeAdapterID: l.AdapterID, RuntimeAdapterMode: string(l.Adapter.Mode), SourceHandler: l.Adapter.SourceHandler, RootfsHandler: l.Adapter.RootfsHandler, RuntimeType: l.Adapter.RuntimeType, DelegateBinary: l.Adapter.DelegateBinary,
		ClusterAdapterDigest: l.Adapter.ClusterAdapterDigest, NodeResolvedDigest: l.Adapter.NodeResolvedDigest, RootfsEnabled: true,
	}
	return &rootfsCreate{lifecycle: l, state: state, source: source, resolve: resolve, response: response}, nil
}

func (c *rootfsCreate) save() error {
	return rootfsstate.Save(c.lifecycle.Config.RuntimeRoot, c.state.ContainerID, c.state)
}

func (c *rootfsCreate) mount() error {
	l, state := c.lifecycle, &c.state
	var err error
	state.Wrapper, err = l.Identity(os.Getpid())
	if err != nil {
		return err
	}
	state.Shim, err = l.Identity(l.DelegatePID)
	if err != nil {
		return err
	}
	state.MountNamespace, err = l.MountNamespace()
	if err != nil {
		return err
	}
	if err := c.save(); err != nil {
		return err
	}
	if err := l.Mounter.Probe(c.response.UpperDir); err != nil {
		return err
	}
	c.mounted = true
	if err := l.Mounter.MountFlat(runtimewrapper.FlatOverlayMountRequest{LowerDirs: c.source.LowerDirs, UpperDir: c.response.UpperDir, WorkDir: c.response.WorkDir, MergedDir: state.MergedDir, ResolverOptions: c.response.MountOptions, MountLabel: c.resolve.MountLabel}); err != nil {
		return err
	}
	state.Phase = rootfsstate.PhaseMounted
	return c.save()
}

func (c *rootfsCreate) register(ctx context.Context) error {
	l, state := c.lifecycle, &c.state
	ack, err := l.Rootfs.MarkMounted(ctx, api.MarkMountedRequest{LeaseID: state.LeaseID, RuntimeStatePath: rootfsstate.Path(l.Config.RuntimeRoot, state.ContainerID)})
	if err != nil || !ack.Mounted {
		return fmt.Errorf("mark Kata rootfs mounted: %v", err)
	}
	begin, err := l.Rootfs.BeginCreate(ctx, api.BeginCreateRequest{LeaseID: state.LeaseID, ContainerID: state.ContainerID, WrapperPID: state.Wrapper.PID, WrapperStartTime: state.Wrapper.StartTime})
	if err != nil || begin.AttemptToken == "" {
		return fmt.Errorf("begin Kata create: %v", err)
	}
	state.Phase, state.AttemptStatus, state.AttemptToken = rootfsstate.PhaseCreatingPending, rootfsstate.AttemptPending, begin.AttemptToken
	if err := c.save(); err != nil {
		return err
	}
	registered, err := l.Rootfs.RegisterDelegate(ctx, api.RegisterDelegateRequest{LeaseID: state.LeaseID, AttemptToken: state.AttemptToken, DelegatePID: state.Shim.PID, DelegateStartTime: state.Shim.StartTime})
	if err != nil || !registered.Registered {
		return fmt.Errorf("register Kata shim: %v", err)
	}
	state.Phase, state.AttemptStatus, state.Delegate = rootfsstate.PhaseCreatingRegistered, rootfsstate.AttemptRegistered, state.Shim
	return c.save()
}

func (c *rootfsCreate) run(ctx context.Context, delegate task.TaskService, req *task.CreateTaskRequest) (*task.CreateTaskResponse, error) {
	if err := c.mount(); err != nil {
		return nil, err
	}
	if err := c.register(ctx); err != nil {
		return nil, err
	}
	forwarded := &task.CreateTaskRequest{}
	proto.Merge(forwarded, req)
	forwarded.Rootfs = []*types.Mount{{Type: "bind", Source: c.state.MergedDir, Options: []string{"rbind", "rw"}}}
	c.called = true
	created, err := delegate.Create(ctx, forwarded)
	if err != nil {
		return nil, err
	}
	committed, err := c.lifecycle.Rootfs.CommitCreate(ctx, api.CommitCreateRequest{LeaseID: c.state.LeaseID, AttemptToken: c.state.AttemptToken})
	if err != nil || !committed.Created {
		return nil, fmt.Errorf("commit Kata create: %v", err)
	}
	c.state.Phase = rootfsstate.PhaseCreated
	if err := c.save(); err != nil {
		return nil, err
	}
	return created, nil
}

func (c *rootfsCreate) rollback(ctx context.Context, delegate task.TaskService, cause error) error {
	l, state := c.lifecycle, c.state
	// An RPC error may follow a successful Create in Kata. Require a successful
	// Delete (or NotFound) before unmounting/releasing the persistent write layer.
	if c.called {
		if _, err := delegate.Delete(context.WithoutCancel(ctx), &task.DeleteRequest{ID: state.ContainerID}); err != nil && !isNotFound(err) {
			return errors.Join(cause, fmt.Errorf("Kata create rollback delete: %w", err))
		}
	}
	if c.mounted {
		if err := l.Unmounter.UnmountNormal(state.MergedDir); err != nil {
			return errors.Join(cause, err)
		}
		if err := l.Verifier.ConfirmUnmounted(state.MergedDir); err != nil {
			return errors.Join(cause, err)
		}
	}
	released, err := l.Rootfs.ReleaseRootfs(context.WithoutCancel(ctx), api.ReleaseRootfsRequest{LeaseID: state.LeaseID, ContainerID: state.ContainerID})
	if err != nil || !released.Released {
		return errors.Join(cause, fmt.Errorf("Kata rollback lease release: %v", err))
	}
	return errors.Join(cause, os.RemoveAll(filepath.Join(l.Config.RuntimeRoot, state.ContainerID)))
}

func (l *RootfsLifecycle) Delete(ctx context.Context, delegate task.TaskService, req *task.DeleteRequest) (*task.DeleteResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if req.ExecID != "" {
		return delegate.Delete(ctx, req)
	}
	state, err := rootfsstate.Load(l.Config.RuntimeRoot, req.ID)
	if os.IsNotExist(err) {
		return delegate.Delete(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if state.FormatVersion != rootfsstate.FormatShimV1 || state.RuntimeAdapterID != l.AdapterID || state.DelegateBinary != l.Adapter.DelegateBinary {
		return nil, fmt.Errorf("Kata delegation state mismatch")
	}
	state.Phase = rootfsstate.PhaseDeleting
	if err := rootfsstate.Save(l.Config.RuntimeRoot, req.ID, state); err != nil {
		return nil, err
	}
	response, err := delegate.Delete(ctx, req)
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	if err := l.Unmounter.UnmountNormal(state.MergedDir); err != nil {
		return nil, err
	}
	if err := l.Verifier.ConfirmUnmounted(state.MergedDir); err != nil {
		return nil, err
	}
	released, err := l.Rootfs.ReleaseRootfs(ctx, api.ReleaseRootfsRequest{LeaseID: state.LeaseID, ContainerID: req.ID})
	if err != nil {
		return nil, err
	}
	if !released.Released {
		return nil, fmt.Errorf("Kata rootfs lease release was not acknowledged")
	}
	if err := os.RemoveAll(filepath.Join(l.Config.RuntimeRoot, req.ID)); err != nil {
		return nil, err
	}
	if response == nil {
		response = &task.DeleteResponse{}
	}
	return response, nil
}
