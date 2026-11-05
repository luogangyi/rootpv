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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

const (
	defaultResolveRetryTimeout  = 30 * time.Second
	defaultResolveRetryInterval = 500 * time.Millisecond
)

type SyscallExecutor struct{}

func (SyscallExecutor) Exec(path string, args []string, env []string) error {
	return syscall.Exec(path, args, env)
}

type Wrapper struct {
	Config             Config
	Rootfs             RootfsClient
	Resolver           Resolver
	Mounter            OverlayMounter
	Unmounter          Unmounter
	SnapshotSource     SnapshotSource
	FlatMounter        FlatOverlayMounter
	NormalUnmounter    NormalUnmounter
	DelegateSupervisor DelegateSupervisor
	CommandRunner      CommandRunner
	RuntimeCleaner     RuntimeCleaner
	UnmountVerifier    UnmountVerifier
	ProcessIdentity    func() (rootfsstate.ProcessIdentity, error)
	ExecutablePath     func() (string, error)
	MountInfoPath      string
	RuntimeStateStore  RuntimeStateStore
	Executor           Executor
	SpecStore          SpecStore
	Logger             Logger

	ResolveRetryTimeout  time.Duration
	ResolveRetryInterval time.Duration
}

type flatCreateRequest struct {
	delegate  string
	args      []string
	env       []string
	inv       Invocation
	spec      *specs.Spec
	specStore SpecStore
	resolve   apiv1alpha1.ResolveRootfsRequest
}

// flatCreateTransaction 保存 Flat Overlay 创建过程的可回滚状态。
// 创建链路会跨越 rootfsd、挂载和 OCI spec 三类资源，所有阶段共享该事务对象，
// 以便在任意一步失败时按既定顺序恢复宿主机状态。
type flatCreateTransaction struct {
	wrapper        Wrapper
	ctx            context.Context
	create         flatCreateRequest
	source         SourceOverlay
	response       apiv1alpha1.ResolveRootfsResponse
	state          rootfsstate.State
	originalRoot   specs.Root
	specSaved      bool
	mountAttempted bool
}

type nestedCreateRequest struct {
	delegate  string
	args      []string
	env       []string
	inv       Invocation
	spec      *specs.Spec
	specStore SpecStore
	resolve   apiv1alpha1.ResolveRootfsRequest
}

type cutoverRootfsRequest struct {
	args       []string
	env        []string
	inv        Invocation
	delegation rootfsstate.State
	adapter    ResolvedRuntimeAdapter
}

type Logger interface {
	Print(v ...any)
}

type RuntimeStateStore interface {
	Load(runtimeRoot, containerID string) (rootfsstate.State, error)
	Save(runtimeRoot, containerID string, state rootfsstate.State) error
	Remove(runtimeRoot, containerID string) error
}

type delegationStateStore struct {
	store      RuntimeStateStore
	delegation rootfsstate.State
}

func (s delegationStateStore) Load(runtimeRoot, containerID string) (rootfsstate.State, error) {
	return s.store.Load(runtimeRoot, containerID)
}

func (s delegationStateStore) Save(runtimeRoot, containerID string, state rootfsstate.State) error {
	if state.FormatVersion == rootfsstate.FormatFlatV1 {
		// Keep the flat transaction's phase and process identities recoverable
		// while pinning the validated adapter before every persistent save.
		state = mergeDelegationRouting(state, s.delegation)
		if state.Phase == rootfsstate.PhaseCreated {
			state.FormatVersion = rootfsstate.FormatDelegationV1
		}
	} else {
		state = mergeDelegationState(state, s.delegation)
	}
	return s.store.Save(runtimeRoot, containerID, state)
}

func (s delegationStateStore) Remove(runtimeRoot, containerID string) error {
	return s.store.Remove(runtimeRoot, containerID)
}

type FileRuntimeStateStore struct{}

func (FileRuntimeStateStore) Load(runtimeRoot, containerID string) (rootfsstate.State, error) {
	return rootfsstate.Load(runtimeRoot, containerID)
}

func (FileRuntimeStateStore) Save(runtimeRoot, containerID string, state rootfsstate.State) error {
	return rootfsstate.Save(runtimeRoot, containerID, state)
}

func (FileRuntimeStateStore) Remove(runtimeRoot, containerID string) error {
	return os.RemoveAll(filepath.Join(runtimeRoot, containerID))
}

func NewWrapper(cfg Config) Wrapper {
	return Wrapper{
		Config:          cfg,
		Rootfs:          NewUnixResolver(cfg.RootfsdSocket),
		Mounter:         SyscallOverlayMounter{},
		Unmounter:       SyscallUnmounter{},
		Executor:        SyscallExecutor{},
		SpecStore:       FileSpecStore{},
		Logger:          log.New(os.Stderr, "rootfs-runtime-wrapper: ", log.LstdFlags),
		FlatMounter:     &SyscallFlatOverlayMounter{},
		NormalUnmounter: SyscallNormalUnmounter{},
		SnapshotSource: LazyContainerdSnapshotSource{
			Address:   cfg.ContainerdAddress,
			Namespace: cfg.ContainerdNamespace,
		},
		DelegateSupervisor: ProcessDelegateSupervisor{Stdout: os.Stdout, Stderr: os.Stderr},
		CommandRunner:      ExecCommandRunner{Stdout: os.Stdout, Stderr: os.Stderr},
		RuntimeCleaner:     ExecRuntimeCleaner{},
		UnmountVerifier:    ProcUnmountVerifier{MountInfoPath: "/proc/self/mountinfo"},
		ProcessIdentity:    CurrentProcessIdentity,
		ExecutablePath:     currentExecutablePath,
		MountInfoPath:      "/proc/self/mountinfo",
		RuntimeStateStore:  FileRuntimeStateStore{},
	}
}

// Run 是 OCI runtime wrapper 的统一入口：根据命令阶段选择创建、清理或直接委托，
// 并只在被 Pod 注解明确启用时将容器根目录改为持久化 Overlay。
func (w Wrapper) Run(ctx context.Context, wrapperName string, args []string, env []string) error {
	if handled, err := w.runDelegateHelper(ctx, args, env); handled {
		return err
	}
	if w.Config.RuntimeAdapterCutover {
		return w.runCutover(ctx, wrapperName, args, env)
	}
	return w.runLegacy(ctx, wrapperName, args, env)
}

// runDelegateHelper 在被 supervisor 作为子进程启动时完成 FD 绑定和登记握手。
func (w Wrapper) runDelegateHelper(ctx context.Context, args, env []string) (bool, error) {
	helper, handled, err := parseDelegateHelper(args, env)
	if !handled {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	notify := os.NewFile(3, "rootpv-delegate-notify")
	ack := os.NewFile(4, "rootpv-delegate-ack")
	if notify == nil || ack == nil {
		return true, fmt.Errorf("delegate helper pipes are unavailable")
	}
	defer notify.Close()
	defer ack.Close()
	helper.Notify = notify
	helper.Ack = ack
	identity := w.ProcessIdentity
	if identity == nil {
		identity = CurrentProcessIdentity
	}
	return true, RunDelegateHelper(ctx, w.Rootfs, helper, identity, w.Executor)
}

func (w Wrapper) runLegacy(ctx context.Context, wrapperName string, args, env []string) error {
	if w.Config.DefaultDelegateRuntime == "" {
		w.Config = DefaultConfig()
	}
	delegate, err := w.Config.DelegateRuntime(wrapperName)
	if err != nil {
		return err
	}
	inv, err := ParseInvocation(args)
	if err != nil {
		return err
	}
	if !inv.Handled {
		return w.exec(delegate, args, env)
	}
	if inv.NeedsCleanup() {
		return w.runLegacyCleanup(ctx, delegate, args, env, inv)
	}
	if !inv.NeedsMount() {
		return w.exec(delegate, args, env)
	}
	return w.runLegacyMount(ctx, delegate, args, env, inv)
}

func (w Wrapper) runLegacyCleanup(ctx context.Context, delegate string, args, env []string, inv Invocation) error {
	state, stateErr := w.stateStore().Load(w.Config.RuntimeRoot, inv.ContainerID)
	if stateErr == nil && state.FormatVersion == rootfsstate.FormatFlatV1 {
		return w.runFlatDelete(ctx, delegate, args, env, state)
	}
	if err := w.cleanup(inv.ContainerID); err != nil {
		w.log(fmt.Sprintf("persistent_rootfs_cleanup_failed container_id=%s err=%v", inv.ContainerID, err))
	}
	if stateErr == nil {
		if err := w.release(ctx, state); err != nil {
			w.log(fmt.Sprintf("persistent_rootfs_release_failed container_id=%s err=%v", inv.ContainerID, err))
		}
	}
	return w.exec(delegate, args, env)
}

func (w Wrapper) runLegacyMount(ctx context.Context, delegate string, args, env []string, inv Invocation) error {
	if inv.Bundle == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		inv.Bundle = cwd
	}
	specStore := w.SpecStore
	if specStore == nil {
		specStore = FileSpecStore{}
	}
	spec, err := specStore.Load(inv.Bundle)
	if err != nil {
		return err
	}
	if !ShouldMountPersistentRootfs(spec) {
		return w.exec(delegate, args, env)
	}
	req, err := BuildResolveRequest(spec, inv)
	if err != nil {
		return err
	}
	if w.Config.OverlayMode == OverlayModeFlatLowerdirs {
		if inv.Command != "create" {
			return fmt.Errorf("runtime_command_unsupported: flat persistent rootfs supports create, got %q", inv.Command)
		}
		return w.runFlatCreate(ctx, flatCreateRequest{delegate: delegate, args: args, env: env, inv: inv, spec: spec, specStore: specStore, resolve: req})
	}
	return w.runNestedCreate(ctx, nestedCreateRequest{delegate: delegate, args: args, env: env, inv: inv, spec: spec, specStore: specStore, resolve: req})
}

func (w Wrapper) runNestedCreate(ctx context.Context, create nestedCreateRequest) error {
	delegate, args, env, inv := create.delegate, create.args, create.env, create.inv
	spec, specStore, req := create.spec, create.specStore, create.resolve
	resp, err := w.resolve(ctx, req)
	if err != nil {
		w.log(fmt.Sprintf("persistent_rootfs_resolve_failed container_id=%s bundle=%s runtime_class=%s delegate_runtime=%s pod_uid=%s namespace=%s pod=%s container=%s lower=%s fail_closed_reason=resolve_failed err=%v", inv.ContainerID, inv.Bundle, req.RuntimeClass, delegate, req.PodUID, req.Namespace, req.PodName, req.ContainerName, req.OriginalRootPath, err))
		return err
	}
	if !resp.Enabled {
		if resp.FallbackToDefaultOverlay {
			return w.exec(delegate, args, env)
		}
		w.log(fmt.Sprintf("persistent_rootfs_disabled container_id=%s bundle=%s runtime_class=%s delegate_runtime=%s pod_uid=%s namespace=%s pod=%s container=%s lower=%s fail_closed_reason=not_enabled reason=%q", inv.ContainerID, inv.Bundle, req.RuntimeClass, delegate, req.PodUID, req.Namespace, req.PodName, req.ContainerName, req.OriginalRootPath, resp.Reason))
		return fmt.Errorf("persistent rootfs is not enabled: %s", resp.Reason)
	}
	// Nested 模式将原始 rootfs 作为下层，持久化 PVC 中的 upper/work 作为写层。
	merged := filepath.Join(w.Config.RuntimeRoot, inv.ContainerID, "merged")
	if err := w.Mounter.MountOverlay(req.OriginalRootPath, resp.UpperDir, resp.WorkDir, merged, resp.MountOptions); err != nil {
		w.log(fmt.Sprintf("persistent_rootfs_mount_failed container_id=%s bundle=%s runtime_class=%s delegate_runtime=%s pod_uid=%s namespace=%s pod=%s container=%s lower=%s upper=%s work=%s merged=%s mount_result=failed fail_closed_reason=overlay_mount_failed err=%v", inv.ContainerID, inv.Bundle, req.RuntimeClass, delegate, req.PodUID, req.Namespace, req.PodName, req.ContainerName, req.OriginalRootPath, resp.UpperDir, resp.WorkDir, merged, err))
		return err
	}
	w.log(fmt.Sprintf("persistent_rootfs_mount container_id=%s bundle=%s runtime_class=%s delegate_runtime=%s pod_uid=%s namespace=%s pod=%s container=%s lower=%s upper=%s work=%s merged=%s mount_result=success", inv.ContainerID, inv.Bundle, req.RuntimeClass, delegate, req.PodUID, req.Namespace, req.PodName, req.ContainerName, req.OriginalRootPath, resp.UpperDir, resp.WorkDir, merged))
	if err := RewriteRoot(spec, merged); err != nil {
		return w.cleanupOnFailure(inv.ContainerID, err)
	}
	if err := specStore.Save(inv.Bundle, spec); err != nil {
		return w.cleanupOnFailure(inv.ContainerID, err)
	}
	if err := w.stateStore().Save(w.Config.RuntimeRoot, inv.ContainerID, rootfsstate.State{
		FormatVersion: rootfsstate.FormatNestedV1,
		ContainerID:   inv.ContainerID,
		Namespace:     req.Namespace,
		PodName:       req.PodName,
		PodUID:        req.PodUID,
		ContainerName: req.ContainerName,
		UpperDir:      resp.UpperDir,
		WorkDir:       resp.WorkDir,
		MetaDir:       resp.MetaDir,
		MergedDir:     merged,
	}); err != nil {
		return w.cleanupOnFailure(inv.ContainerID, err)
	}
	if err := w.exec(delegate, args, env); err != nil {
		return w.cleanupOnFailure(inv.ContainerID, err)
	}
	return nil
}

// cleanupOnFailure 保留原始失败原因，同时将卸载或租约释放失败一并返回，避免资源清理错误被静默丢弃。
func (w Wrapper) cleanupOnFailure(containerID string, cause error) error {
	if cleanupErr := w.cleanup(containerID); cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("cleanup persistent rootfs: %w", cleanupErr))
	}
	return cause
}

// runCutover 使用节点生成的、不可变的 adapter registry 恢复原始 runtime，
// 同时持久化委托信息，使后续 state/delete 命令无需重新依赖 OCI spec。
func (w Wrapper) runCutover(ctx context.Context, wrapperName string, args, env []string) error {
	inv, err := ParseInvocation(args)
	if err != nil {
		return err
	}
	if !inv.Handled {
		return fmt.Errorf("runtime_adapter_state_mismatch: command cannot be routed without container delegation state")
	}
	if inv.NeedsCleanup() {
		return w.runCutoverCleanup(ctx, args, env, inv)
	}
	if !inv.NeedsMount() {
		return w.runCutoverDelegate(args, env, inv)
	}
	return w.runCutoverCreate(ctx, wrapperName, args, env, inv)
}

func (w Wrapper) runCutoverCleanup(ctx context.Context, args, env []string, inv Invocation) error {
	state, err := w.stateStore().Load(w.Config.RuntimeRoot, inv.ContainerID)
	if err != nil || state.DelegateBinary == "" {
		return fmt.Errorf("delegation_state_missing")
	}
	if state.RuntimeType == config.RunscRuntimeType && state.RootfsEnabled && state.LeaseID != "" {
		if state.MergedDir == "" {
			return fmt.Errorf("runsc persistent rootfs state has no merged directory")
		}
		return w.runFlatDelete(ctx, state.DelegateBinary, args, env, state)
	}
	return w.legacyDelegateWrapper(state.DelegateBinary).Run(ctx, "rootfs-runtime-wrapper", args, env)
}

func (w Wrapper) runCutoverDelegate(args, env []string, inv Invocation) error {
	state, err := w.stateStore().Load(w.Config.RuntimeRoot, inv.ContainerID)
	if err != nil || state.FormatVersion != rootfsstate.FormatDelegationV1 || state.DelegateBinary == "" {
		return fmt.Errorf("delegation_state_missing")
	}
	return w.exec(state.DelegateBinary, args, env)
}

func (w Wrapper) runCutoverCreate(ctx context.Context, wrapperName string, args, env []string, inv Invocation) error {
	if inv.Bundle == "" {
		bundle, err := os.Getwd()
		if err != nil {
			return err
		}
		inv.Bundle = bundle
	}
	specStore := w.SpecStore
	if specStore == nil {
		specStore = FileSpecStore{}
	}
	spec, err := specStore.Load(inv.Bundle)
	if err != nil {
		return err
	}
	adapterID, adapter, err := w.resolveCutoverAdapter(wrapperName, spec)
	if err != nil {
		return err
	}
	if !config.IsOCIRuntimeType(adapter.RuntimeType) {
		return fmt.Errorf("OCI wrapper cannot delegate task shim %q", adapter.RuntimeType)
	}
	if adapter.RuntimeType == config.RunscRuntimeType && ShouldMountPersistentRootfs(spec) {
		if err := validateRunscCreate(w.Config, spec, args); err != nil {
			return err
		}
	}
	delegation := newDelegationState(inv.ContainerID, adapterID, adapter, ShouldMountPersistentRootfs(spec))
	if err := w.stateStore().Save(w.Config.RuntimeRoot, inv.ContainerID, delegation); err != nil {
		return err
	}
	if !ShouldMountPersistentRootfs(spec) {
		return w.exec(adapter.DelegateBinary, args, env)
	}
	return w.runCutoverRootfs(ctx, cutoverRootfsRequest{args: args, env: env, inv: inv, delegation: delegation, adapter: adapter})
}

func (w Wrapper) resolveCutoverAdapter(wrapperName string, spec *specs.Spec) (string, ResolvedRuntimeAdapter, error) {
	annotations := specAnnotations(spec)
	registry, err := LoadAdapterRegistry(w.Config.RuntimeAdapterRegistry, RegistryOptions{AllowedDelegateDirs: w.Config.AllowedDelegateDirs})
	if err != nil {
		return "", ResolvedRuntimeAdapter{}, err
	}
	adapterID := annotations[rootfs.AnnotationRuntimeAdapterID]
	adapterMode := config.RuntimeAdapterMode(annotations[rootfs.AnnotationRuntimeAdapterMode])
	adapterDigest := annotations[rootfs.AnnotationRuntimeAdapterConfigHash]
	var adapter ResolvedRuntimeAdapter
	if adapterID == "" && adapterMode == "" && adapterDigest == "" && IsSandbox(spec) {
		// CRI creates the sandbox before it creates the admitted workload
		// container. At this point its OCI spec carries the generated runtime
		// handler but not the admission annotations. The generated handler is
		// node-local, immutable registry evidence and maps to exactly one
		// resolved adapter.
		handler := labelValue(annotations, "io.kubernetes.cri.sandbox-runtime-handler", "io.kubernetes.cri.runtime-handler")
		if handler != "" {
			adapterID, adapter, err = registry.ResolveByRootfsHandler(handler)
		} else {
			adapterID, adapter, err = registry.ResolveByWrapperBinary(wrapperName)
		}
	} else {
		adapter, err = registry.Resolve(adapterID, adapterMode, adapterDigest)
	}
	if err != nil {
		return "", ResolvedRuntimeAdapter{}, err
	}
	if adapter.RootfsRuntimeClass != "" && annotations[rootfs.AnnotationEffectiveRuntimeClass] != "" && annotations[rootfs.AnnotationEffectiveRuntimeClass] != adapter.RootfsRuntimeClass {
		return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_state_mismatch")
	}
	// containerd keeps the sandbox runtime handler in CRI metadata; it is not
	// guaranteed to be copied into the OCI spec delivered to the runtime binary.
	// Validate it whenever present, while the admitted effective RuntimeClass and
	// registry entry remain the mandatory, immutable routing evidence.
	if handler := labelValue(annotations, "io.kubernetes.cri.sandbox-runtime-handler"); handler != "" && handler != adapter.RootfsHandler {
		return "", ResolvedRuntimeAdapter{}, fmt.Errorf("runtime_adapter_state_mismatch")
	}
	return adapterID, adapter, nil
}

func newDelegationState(containerID, adapterID string, adapter ResolvedRuntimeAdapter, rootfsEnabled bool) rootfsstate.State {
	return rootfsstate.State{
		FormatVersion:        rootfsstate.FormatDelegationV1,
		ContainerID:          containerID,
		RuntimeAdapterID:     adapterID,
		RuntimeAdapterMode:   string(adapter.Mode),
		SourceHandler:        adapter.SourceHandler,
		RootfsHandler:        adapter.RootfsHandler,
		RuntimeType:          adapter.RuntimeType,
		DelegateBinary:       adapter.DelegateBinary,
		ClusterAdapterDigest: adapter.ClusterAdapterDigest,
		NodeResolvedDigest:   adapter.NodeResolvedDigest,
		RootfsEnabled:        rootfsEnabled,
		Phase:                rootfsstate.PhaseCreated,
	}
}

func (w Wrapper) runCutoverRootfs(ctx context.Context, request cutoverRootfsRequest) error {
	legacy := w.legacyDelegateWrapper(request.adapter.DelegateBinary)
	legacy.RuntimeStateStore = delegationStateStore{store: w.stateStore(), delegation: request.delegation}
	if legacy.Config.OverlayMode != OverlayModeFlatLowerdirs {
		return legacy.Run(ctx, "rootfs-runtime-wrapper", request.args, request.env)
	}
	if err := legacy.Run(ctx, "rootfs-runtime-wrapper", request.args, request.env); err != nil {
		return err
	}
	state, err := w.stateStore().Load(w.Config.RuntimeRoot, request.inv.ContainerID)
	if err != nil {
		return err
	}
	return w.stateStore().Save(w.Config.RuntimeRoot, request.inv.ContainerID, mergeDelegationState(state, request.delegation))
}

// mergeDelegationState 保留 Overlay 生命周期字段，并用本次已校验的 adapter 证据覆盖路由字段。
func mergeDelegationState(state, delegation rootfsstate.State) rootfsstate.State {
	state.FormatVersion = rootfsstate.FormatDelegationV1
	state.Phase = rootfsstate.PhaseCreated
	return mergeDelegationRouting(state, delegation)
}

func mergeDelegationRouting(state, delegation rootfsstate.State) rootfsstate.State {
	state.RuntimeAdapterID = delegation.RuntimeAdapterID
	state.RuntimeAdapterMode = delegation.RuntimeAdapterMode
	state.SourceHandler = delegation.SourceHandler
	state.RootfsHandler = delegation.RootfsHandler
	state.RuntimeType = delegation.RuntimeType
	state.DelegateBinary = delegation.DelegateBinary
	state.ClusterAdapterDigest = delegation.ClusterAdapterDigest
	state.NodeResolvedDigest = delegation.NodeResolvedDigest
	state.RootfsEnabled = delegation.RootfsEnabled
	return state
}

// legacyDelegateWrapper 复用原有 Overlay 创建/清理流程，但关闭 cutover 以避免递归路由。
func (w Wrapper) legacyDelegateWrapper(delegate string) Wrapper {
	legacy := w
	legacy.Config.RuntimeAdapterCutover = false
	legacy.Config.DefaultDelegateRuntime = delegate
	legacy.Config.DelegateRuntimeByWrapper = map[string]string{"rootfs-runtime-wrapper": delegate}
	return legacy
}

// runFlatDelete normally unmounts before runtime deletion; runsc first closes
// its gofer, then unmounts and verifies the rootfs before releasing the lease.
// 若任一步失败即停止，避免在挂载仍存在时解除 upperdir 的独占锁。
func (w Wrapper) runFlatDelete(ctx context.Context, delegate string, args, env []string, state rootfsstate.State) error {
	state, err := w.prepareFlatDelete(state, args)
	if err != nil {
		return err
	}
	if err := w.stateStore().Save(w.Config.RuntimeRoot, state.ContainerID, state); err != nil {
		return err
	}
	if state.DelegateBinary != "" {
		delegate = state.DelegateBinary
	}
	if state.RuntimeType == config.RunscRuntimeType {
		if err := w.cleanRunsc(ctx, delegate, state, state.DeleteArgs, env); err != nil {
			return err
		}
	}
	if err := w.NormalUnmounter.UnmountNormal(state.MergedDir); err != nil {
		return fmt.Errorf("normal unmount persistent rootfs: %w", err)
	}
	if err := w.UnmountVerifier.ConfirmUnmounted(state.MergedDir); err != nil {
		return err
	}
	if state.RuntimeType != config.RunscRuntimeType {
		if err := w.CommandRunner.Run(ctx, delegate, args, env); err != nil {
			return fmt.Errorf("delegate delete: %w", err)
		}
	}
	released, err := w.Rootfs.ReleaseRootfs(ctx, apiv1alpha1.ReleaseRootfsRequest{LeaseID: state.LeaseID, ContainerID: state.ContainerID})
	if err != nil {
		return err
	}
	if !released.Released {
		return fmt.Errorf("rootfsd did not confirm lease release")
	}
	if err := w.stateStore().Remove(w.Config.RuntimeRoot, state.ContainerID); err != nil {
		return err
	}
	return nil
}

// prepareFlatDelete pins validated runsc delete arguments before any runtime or
// mount side effects, so recovery uses the same root even for legacy states.
func (w Wrapper) prepareFlatDelete(state rootfsstate.State, args []string) (rootfsstate.State, error) {
	if w.NormalUnmounter == nil || w.UnmountVerifier == nil || w.Rootfs == nil {
		return state, fmt.Errorf("flat rootfs delete dependencies are required")
	}
	if state.RuntimeType == config.RunscRuntimeType {
		if w.RuntimeCleaner == nil {
			return state, fmt.Errorf("partial runtime cleaner is required")
		}
		deleteArgs, err := runscDeleteArgs(state, args)
		if err != nil {
			return state, err
		}
		state.DeleteArgs = deleteArgs
	} else {
		if w.CommandRunner == nil {
			return state, fmt.Errorf("flat rootfs delete command runner is required")
		}
	}
	state.Phase = rootfsstate.PhaseDeleting
	return state, nil
}

// runFlatCreate 以 containerd 的只读快照链为下层、PVC 为写层创建 Flat Overlay，
// 并通过 rootfsd 租约和状态文件将挂载、runtime 创建、恢复清理串成可恢复事务。
func (w Wrapper) runFlatCreate(ctx context.Context, create flatCreateRequest) error {
	txn, err := w.newFlatCreateTransaction(ctx, create)
	if err != nil {
		return err
	}
	if err := txn.mount(); err != nil {
		return txn.rollback(err)
	}
	return txn.createRuntime()
}

// newFlatCreateTransaction 在修改宿主机前收集可信快照信息、rootfsd 租约和进程证据。
func (w Wrapper) newFlatCreateTransaction(ctx context.Context, create flatCreateRequest) (*flatCreateTransaction, error) {
	if err := w.validateFlatCreateDependencies(); err != nil {
		return nil, err
	}
	identity, binary, err := w.flatWrapperEvidence()
	if err != nil {
		return nil, err
	}
	source, response, err := w.resolveFlatSource(ctx, create)
	if err != nil {
		return nil, err
	}
	state := w.newFlatRuntimeState(create, source, response, identity, binary)
	return &flatCreateTransaction{
		wrapper: w, ctx: ctx, create: create, source: source, response: response,
		state: state, originalRoot: *create.spec.Root,
	}, nil
}

// validateFlatCreateDependencies 确保 Flat 模式的外部依赖均已配置，避免中途半初始化。
func (w Wrapper) validateFlatCreateDependencies() error {
	if w.Rootfs == nil {
		return fmt.Errorf("flat rootfs requires rootfsd lifecycle client")
	}
	if w.SnapshotSource == nil {
		return fmt.Errorf("flat rootfs requires containerd snapshot source")
	}
	if w.FlatMounter == nil || w.NormalUnmounter == nil {
		return fmt.Errorf("flat rootfs requires flat mounter and normal unmounter")
	}
	if w.DelegateSupervisor == nil {
		return fmt.Errorf("flat rootfs requires delegate supervisor")
	}
	return nil
}

// flatWrapperEvidence 获取可用于恢复的 wrapper 身份和绝对可执行文件路径。
func (w Wrapper) flatWrapperEvidence() (rootfsstate.ProcessIdentity, string, error) {
	identityFn := w.ProcessIdentity
	if identityFn == nil {
		identityFn = CurrentProcessIdentity
	}
	wrapperIdentity, err := identityFn()
	if err != nil {
		return rootfsstate.ProcessIdentity{}, "", fmt.Errorf("read wrapper process identity: %w", err)
	}
	if !wrapperIdentity.Valid() {
		return rootfsstate.ProcessIdentity{}, "", fmt.Errorf("wrapper process identity is invalid")
	}
	executablePath := w.ExecutablePath
	if executablePath == nil {
		executablePath = currentExecutablePath
	}
	wrapperBinary, err := executablePath()
	if err != nil {
		return rootfsstate.ProcessIdentity{}, "", fmt.Errorf("resolve wrapper executable: %w", err)
	}
	if wrapperBinary == "" || !filepath.IsAbs(wrapperBinary) {
		return rootfsstate.ProcessIdentity{}, "", fmt.Errorf("resolved wrapper executable must be an absolute path")
	}
	return wrapperIdentity, wrapperBinary, nil
}

// resolveFlatSource 使用 containerd 返回的实际快照身份请求 PVC 租约，调用方不能伪造下层路径。
func (w Wrapper) resolveFlatSource(ctx context.Context, create flatCreateRequest) (SourceOverlay, apiv1alpha1.ResolveRootfsResponse, error) {
	// 从 containerd 查询当前容器实际使用的快照及 chain ID，禁止由调用方伪造下层信息。
	source, err := w.SnapshotSource.Resolve(ctx, create.inv.ContainerID)
	if err != nil {
		return SourceOverlay{}, apiv1alpha1.ResolveRootfsResponse{}, err
	}
	request := create.resolve
	request.Snapshotter = source.Snapshotter
	request.SnapshotKey = source.SnapshotKey
	request.RootfsChainID = source.RootfsChainID
	response, err := w.resolve(ctx, request)
	if err != nil {
		return SourceOverlay{}, apiv1alpha1.ResolveRootfsResponse{}, err
	}
	if !response.Enabled || response.LeaseID == "" {
		return SourceOverlay{}, apiv1alpha1.ResolveRootfsResponse{}, fmt.Errorf("flat rootfs resolve did not return an enabled lease: %s", response.Reason)
	}
	return source, response, nil
}

// newFlatRuntimeState 建立尚未挂载时的状态记录，供故障恢复识别本次租约。
func (w Wrapper) newFlatRuntimeState(create flatCreateRequest, source SourceOverlay, response apiv1alpha1.ResolveRootfsResponse, identity rootfsstate.ProcessIdentity, binary string) rootfsstate.State {
	request, invocation := create.resolve, create.inv
	state := rootfsstate.State{
		FormatVersion:    rootfsstate.FormatFlatV1,
		ContainerID:      invocation.ContainerID,
		Namespace:        request.Namespace,
		PodName:          request.PodName,
		PodUID:           request.PodUID,
		ContainerName:    request.ContainerName,
		LeaseID:          response.LeaseID,
		Phase:            rootfsstate.PhaseAcquired,
		RuntimeNamespace: w.Config.ContainerdNamespace,
		Snapshotter:      source.Snapshotter,
		SnapshotKey:      source.SnapshotKey,
		RootfsChainID:    source.RootfsChainID,
		Bundle:           invocation.Bundle,
		OriginalRootPath: request.OriginalRootPath,
		UpperDir:         response.UpperDir,
		WorkDir:          response.WorkDir,
		MetaDir:          response.MetaDir,
		MergedDir:        filepath.Join(w.Config.RuntimeRoot, invocation.ContainerID, "merged"),
		Wrapper:          identity,
		WrapperBinary:    binary,
		WrapperArgs:      append([]string(nil), create.args...),
		DelegateBinary:   create.delegate,
		DelegateArgs:     append([]string(nil), create.args...),
	}
	if shim, err := ProcessIdentityFor(os.Getppid()); err == nil {
		state.Shim = shim
	}
	if inode, err := CurrentMountNamespaceInode(); err == nil {
		state.MountNamespace = inode
	}
	return state
}

// rollback 将 Flat Overlay 创建失败前已生效的资源逆序释放，始终返回原始失败原因。
func (t *flatCreateTransaction) rollback(cause error) error {
	rollbackErr := t.restoreSpec()
	rollbackErr = t.unmountDuringRollback(rollbackErr)
	rollbackErr = t.releaseLease(rollbackErr)
	rollbackErr = t.removeState(rollbackErr)
	if rollbackErr != nil {
		return errors.Join(cause, rollbackErr)
	}
	return cause
}

func (t *flatCreateTransaction) restoreSpec() error {
	if !t.specSaved {
		return nil
	}
	t.create.spec.Root = &t.originalRoot
	if err := t.create.specStore.Save(t.create.inv.Bundle, t.create.spec); err != nil {
		return fmt.Errorf("restore original spec: %w", err)
	}
	return nil
}

func (t *flatCreateTransaction) unmountDuringRollback(previous error) error {
	if !t.mountAttempted {
		return previous
	}
	if err := t.wrapper.NormalUnmounter.UnmountNormal(t.state.MergedDir); err != nil {
		return errors.Join(previous, fmt.Errorf("normal unmount during rollback: %w", err))
	}
	if previous != nil {
		return previous
	}
	if t.wrapper.UnmountVerifier == nil {
		return fmt.Errorf("unmount verifier is required during rollback")
	}
	if err := t.wrapper.UnmountVerifier.ConfirmUnmounted(t.state.MergedDir); err != nil {
		return fmt.Errorf("confirm rollback unmount: %w", err)
	}
	return nil
}

func (t *flatCreateTransaction) releaseLease(previous error) error {
	if previous != nil {
		return previous
	}
	_, err := t.wrapper.Rootfs.ReleaseRootfs(t.ctx, apiv1alpha1.ReleaseRootfsRequest{LeaseID: t.response.LeaseID, ContainerID: t.create.inv.ContainerID})
	if err != nil {
		return fmt.Errorf("release lease during rollback: %w", err)
	}
	return nil
}

func (t *flatCreateTransaction) removeState(previous error) error {
	if previous != nil {
		return previous
	}
	if err := t.wrapper.stateStore().Remove(t.wrapper.Config.RuntimeRoot, t.create.inv.ContainerID); err != nil {
		return fmt.Errorf("remove runtime state during rollback: %w", err)
	}
	return nil
}

// mount 执行挂载和 spec 改写；失败交由调用方统一回滚。
func (t *flatCreateTransaction) mount() error {
	if err := t.saveState(); err != nil {
		return err
	}
	validated, err := t.validatedSource()
	if err != nil {
		return err
	}
	t.state.SourceMountPoint = validated.MountPoint
	t.state.LowerDirCount = len(validated.LowerDirs)
	if err := t.saveState(); err != nil {
		return err
	}
	if err := t.mountOverlay(validated); err != nil {
		return err
	}
	if err := t.rewriteSpec(); err != nil {
		return err
	}
	return t.markMounted()
}

func (t *flatCreateTransaction) saveState() error {
	return t.wrapper.stateStore().Save(t.wrapper.Config.RuntimeRoot, t.create.inv.ContainerID, t.state)
}

func (t *flatCreateTransaction) validatedSource() (SourceOverlay, error) {
	mountInfoPath := t.wrapper.MountInfoPath
	if mountInfoPath == "" {
		mountInfoPath = "/proc/self/mountinfo"
	}
	mountInfo, err := os.Open(mountInfoPath)
	if err != nil {
		return SourceOverlay{}, fmt.Errorf("open mountinfo: %w", err)
	}
	validated, inspectErr := InspectAndValidateSourceOverlay(mountInfo, t.create.resolve.OriginalRootPath, t.source, t.wrapper.Config.AllowedOverlaySnapshotRoots)
	closeErr := mountInfo.Close()
	if inspectErr != nil {
		return SourceOverlay{}, inspectErr
	}
	if closeErr != nil {
		return SourceOverlay{}, closeErr
	}
	return validated, nil
}

func (t *flatCreateTransaction) mountOverlay(validated SourceOverlay) error {
	if err := t.wrapper.FlatMounter.Probe(t.response.UpperDir); err != nil {
		return err
	}
	t.mountAttempted = true
	return t.wrapper.FlatMounter.MountFlat(FlatOverlayMountRequest{
		LowerDirs: validated.LowerDirs, UpperDir: t.response.UpperDir, WorkDir: t.response.WorkDir,
		MergedDir: t.state.MergedDir, ResolverOptions: t.response.MountOptions, MountLabel: t.create.resolve.MountLabel,
	})
}

func (t *flatCreateTransaction) rewriteSpec() error {
	if err := RewriteRoot(t.create.spec, t.state.MergedDir); err != nil {
		return err
	}
	if err := t.create.specStore.Save(t.create.inv.Bundle, t.create.spec); err != nil {
		return err
	}
	t.specSaved = true
	t.state.Phase = rootfsstate.PhaseMounted
	return t.saveState()
}

func (t *flatCreateTransaction) markMounted() error {
	mounted, err := t.wrapper.Rootfs.MarkMounted(t.ctx, apiv1alpha1.MarkMountedRequest{LeaseID: t.response.LeaseID, RuntimeStatePath: rootfsstate.Path(t.wrapper.Config.RuntimeRoot, t.create.inv.ContainerID)})
	if err != nil {
		return err
	}
	if !mounted.Mounted {
		return fmt.Errorf("rootfsd did not acknowledge mounted state")
	}
	return nil
}

// createRuntime 将已挂载的 Overlay 交给 delegate 创建运行时，并提交 rootfsd 的创建令牌。
func (t *flatCreateTransaction) createRuntime() error {
	begin, err := t.beginCreate()
	if err != nil {
		return t.rollback(err)
	}
	if err := t.runDelegate(begin.AttemptToken); err != nil {
		return t.rollbackAfterDelegate(err)
	}
	committed, err := t.wrapper.Rootfs.CommitCreate(t.ctx, apiv1alpha1.CommitCreateRequest{LeaseID: t.response.LeaseID, AttemptToken: begin.AttemptToken})
	if err != nil {
		return t.rollbackAfterDelegate(err)
	}
	if !committed.Created {
		return t.rollbackAfterDelegate(fmt.Errorf("rootfsd did not acknowledge created state"))
	}
	t.state.Phase = rootfsstate.PhaseCreated
	return t.saveState()
}

func (t *flatCreateTransaction) beginCreate() (apiv1alpha1.BeginCreateResponse, error) {
	begin, err := t.wrapper.Rootfs.BeginCreate(t.ctx, apiv1alpha1.BeginCreateRequest{
		LeaseID: t.response.LeaseID, ContainerID: t.create.inv.ContainerID,
		WrapperPID: t.state.Wrapper.PID, WrapperStartTime: t.state.Wrapper.StartTime,
	})
	if err != nil {
		return apiv1alpha1.BeginCreateResponse{}, err
	}
	if begin.AttemptToken == "" {
		return apiv1alpha1.BeginCreateResponse{}, fmt.Errorf("rootfsd returned an empty create attempt token")
	}
	t.state.Phase = rootfsstate.PhaseCreatingPending
	t.state.AttemptStatus = rootfsstate.AttemptPending
	t.state.AttemptToken = begin.AttemptToken
	return begin, t.saveState()
}

func (t *flatCreateTransaction) runDelegate(attemptToken string) error {
	// supervisor 要求 delegate 先登记身份，父进程持久化后才允许子进程 exec 真正 runtime。
	return t.wrapper.DelegateSupervisor.Run(t.ctx, DelegateRequest{
		LeaseID: t.response.LeaseID, AttemptToken: attemptToken, Delegate: t.create.delegate,
		Args: append([]string(nil), t.create.args...), Env: append([]string(nil), t.create.env...),
	}, func(delegateIdentity rootfsstate.ProcessIdentity) error {
		t.state.Phase = rootfsstate.PhaseCreatingRegistered
		t.state.AttemptStatus = rootfsstate.AttemptRegistered
		t.state.Delegate = delegateIdentity
		return t.saveState()
	})
}

func (t *flatCreateTransaction) rollbackAfterDelegate(cause error) error {
	if t.wrapper.RuntimeCleaner == nil {
		return t.rollback(errors.Join(cause, fmt.Errorf("partial runtime cleaner is required")))
	}
	if err := t.wrapper.RuntimeCleaner.CleanupPartial(t.ctx, t.create.delegate, t.create.inv.ContainerID, t.create.args, t.create.env); err != nil {
		return t.rollback(errors.Join(cause, err))
	}
	return t.rollback(cause)
}

func currentExecutablePath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// cleanup 清理 Nested 模式的本地挂载目录；它尽力执行所有步骤并汇总错误。
func (w Wrapper) cleanup(containerID string) error {
	if containerID == "" {
		return fmt.Errorf("container id is required for cleanup")
	}
	containerDir := filepath.Join(w.Config.RuntimeRoot, containerID)
	merged := filepath.Join(containerDir, "merged")
	var cleanupErr error
	if w.Unmounter != nil {
		if err := w.Unmounter.Unmount(merged); err != nil {
			w.log(fmt.Sprintf("persistent_rootfs_unmount_failed container_id=%s merged=%s err=%v", containerID, merged, err))
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if err := os.RemoveAll(containerDir); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

// exec 用真实 runtime 替换 wrapper 进程，因此成功时不会返回到调用方。
func (w Wrapper) exec(delegate string, args []string, env []string) error {
	if w.Executor == nil {
		return fmt.Errorf("executor is required")
	}
	execArgs := append([]string{delegate}, args...)
	return w.Executor.Exec(delegate, execArgs, env)
}

// resolve 在 rootfsd 启动或 Pod 注册尚未完成的短暂窗口内重试；超时后由调用方失败关闭。
func (w Wrapper) resolve(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	timeout := w.ResolveRetryTimeout
	if timeout == 0 {
		timeout = defaultResolveRetryTimeout
	}
	interval := w.ResolveRetryInterval
	if interval == 0 {
		interval = defaultResolveRetryInterval
	}
	if timeout < 0 {
		timeout = 0
	}
	if interval < 0 {
		interval = 0
	}

	start := time.Now()
	attempts := 0
	for {
		attempts++
		resp, err := w.resolveOnce(ctx, req)
		if err == nil {
			if attempts > 1 {
				w.log(fmt.Sprintf("persistent_rootfs_resolve_retry_succeeded container_id=%s pod_uid=%s namespace=%s pod=%s container=%s attempts=%d elapsed=%s", req.ContainerID, req.PodUID, req.Namespace, req.PodName, req.ContainerName, attempts, time.Since(start).Round(time.Millisecond)))
			}
			return resp, nil
		}
		if timeout == 0 || !isTransientResolveError(err) || time.Since(start) >= timeout {
			return apiv1alpha1.ResolveRootfsResponse{}, err
		}

		sleep := interval
		if remaining := timeout - time.Since(start); remaining < sleep {
			sleep = remaining
		}
		if sleep <= 0 {
			return apiv1alpha1.ResolveRootfsResponse{}, err
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return apiv1alpha1.ResolveRootfsResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (w Wrapper) resolveOnce(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	if w.Rootfs != nil {
		return w.Rootfs.ResolveRootfs(ctx, req)
	}
	return w.Resolver.ResolveRootfs(ctx, req)
}

func isTransientResolveError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, " is not registered") ||
		strings.Contains(msg, "PVC host path for volume") ||
		strings.Contains(msg, "PVC host path is not mounted")
}

func (w Wrapper) release(ctx context.Context, state rootfsstate.State) error {
	if w.Rootfs == nil {
		return nil
	}
	_, err := w.Rootfs.ReleaseRootfs(ctx, apiv1alpha1.ReleaseRootfsRequest{
		ContainerID:   state.ContainerID,
		LeaseID:       state.LeaseID,
		Namespace:     state.Namespace,
		PodName:       state.PodName,
		PodUID:        state.PodUID,
		ContainerName: state.ContainerName,
		MetaDir:       state.MetaDir,
	})
	if err != nil {
		w.log(fmt.Sprintf("persistent_rootfs_release_failed container_id=%s pod_uid=%s container=%s meta=%s err=%v", state.ContainerID, state.PodUID, state.ContainerName, state.MetaDir, err))
		return err
	}
	w.log(fmt.Sprintf("persistent_rootfs_release container_id=%s pod_uid=%s container=%s meta=%s", state.ContainerID, state.PodUID, state.ContainerName, state.MetaDir))
	return nil
}

// log 输出已经由调用方使用固定格式串构造完成的运行日志，避免将外部输入作为格式串传递。
func (w Wrapper) log(message string) {
	if w.Logger != nil {
		w.Logger.Print(message)
	}
}

func (w Wrapper) stateStore() RuntimeStateStore {
	if w.RuntimeStateStore != nil {
		return w.RuntimeStateStore
	}
	return FileRuntimeStateStore{}
}
