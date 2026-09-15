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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/containerd/containerd"
	"github.com/containerd/errdefs"

	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

type ProcessChecker interface {
	Alive(rootfsstate.ProcessIdentity) bool
}

type RecoveryCleaner interface {
	Cleanup(context.Context, rootfsstate.State) error
}

// RuntimeStateChecker 检查 RootPV 容器是否仍被 containerd 作为 task 跟踪。
// containerd 重启后孤儿 shim 或 runc 进程可能暂时仍在，但已脱离 containerd 的 task
// 无法再触发 wrapper 的 delete 路径，因此不能仅依赖进程 PID 或 runc state 判断。
type RuntimeStateChecker interface {
	Running(context.Context, rootfsstate.State) (bool, error)
}

type Reconciler struct {
	RuntimeRoot string
	Leases      *LeaseManager
	Processes   ProcessChecker
	Runtime     RuntimeStateChecker
	Cleaner     RecoveryCleaner
	removeAll   func(string) error
}

type recoveryResult struct {
	recoveredStates map[string]struct{}
	releasedStates  map[string]struct{}
	missingLeases   map[string]struct{}
	err             error
}

func (r Reconciler) Reconcile(ctx context.Context) error {
	if r.RuntimeRoot == "" || r.Leases == nil {
		return fmt.Errorf("reconciler runtime root and lease manager are required")
	}
	states, err := scanRuntimeStates(r.RuntimeRoot)
	if err != nil {
		return err
	}
	recovery := r.recoverStates(ctx, states)
	return errors.Join(recovery.err, r.cleanupRecoveredStates(ctx, states, recovery))
}

// recoverStates 尝试恢复每个状态的租约。单个状态损坏时保留错误并继续，
// 使已释放租约的残留 runtime state 仍可以在本轮清理。
func (r Reconciler) recoverStates(ctx context.Context, states []rootfsstate.State) recoveryResult {
	result := recoveryResult{
		recoveredStates: make(map[string]struct{}),
		releasedStates:  make(map[string]struct{}),
		missingLeases:   make(map[string]struct{}),
	}
	for _, state := range states {
		released, err := r.Leases.Recover(ctx, state)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// PVC 已被 kubelet 删除时，lease 元数据会一并消失。此时没有
				// 可释放的锁，但仍必须清除 host 上可能残留的 runtime/Overlay 状态。
				result.recoveredStates[state.ContainerID] = struct{}{}
				result.missingLeases[state.ContainerID] = struct{}{}
				continue
			}
			result.err = errors.Join(result.err, fmt.Errorf("recover lease for %s: %w", state.ContainerID, err))
			continue
		}
		result.recoveredStates[state.ContainerID] = struct{}{}
		if released {
			result.releasedStates[state.LeaseID] = struct{}{}
		}
	}
	return result
}

func (r Reconciler) cleanupRecoveredStates(ctx context.Context, states []rootfsstate.State, recovery recoveryResult) error {
	processes := r.Processes
	if processes == nil {
		processes = ProcProcessChecker{}
	}
	runtime := r.Runtime
	if runtime == nil {
		runtime = ContainerdRuntimeStateChecker{}
	}
	cleaner := r.Cleaner
	if cleaner == nil {
		cleaner = FailClosedRecoveryCleaner{}
	}
	var cleanupErr error
	for _, state := range states {
		if _, recovered := recovery.recoveredStates[state.ContainerID]; !recovered {
			continue
		}
		_, missingLease := recovery.missingLeases[state.ContainerID]
		if err := r.cleanupRecoveredState(ctx, state, processes, runtime, cleaner, recovery.releasedStates, missingLease); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func (r Reconciler) cleanupRecoveredState(ctx context.Context, state rootfsstate.State, processes ProcessChecker, runtime RuntimeStateChecker, cleaner RecoveryCleaner, releasedStates map[string]struct{}, missingLease bool) error {
	// Release 已成功时，前一轮已经完成了外部资源清理；此处只重试删除
	// runtime state，避免再次撤销或清理已释放的租约。
	if _, released := releasedStates[state.LeaseID]; released {
		if err := r.removeRuntimeState(state.ContainerID); err != nil {
			return fmt.Errorf("remove released runtime state for %s: %w", state.ContainerID, err)
		}
		return nil
	}
	retained, err := retainRecoveredState(ctx, state, processes, runtime)
	if err != nil {
		return fmt.Errorf("inspect recovered runtime state for %s: %w", state.ContainerID, err)
	}
	if retained {
		return nil
	}
	if state.Phase == rootfsstate.PhaseCreatingPending && state.AttemptStatus == rootfsstate.AttemptPending {
		if err := r.Leases.RevokeCreate(ctx, state.LeaseID, state.AttemptToken); err != nil {
			return fmt.Errorf("revoke dead create attempt for %s: %w", state.ContainerID, err)
		}
	}
	if err := cleaner.Cleanup(ctx, state); err != nil {
		return fmt.Errorf("cleanup recovered state for %s: %w", state.ContainerID, err)
	}
	if missingLease {
		return r.removeRuntimeState(state.ContainerID)
	}
	if _, err := r.Leases.Release(ctx, state.LeaseID); err != nil {
		return fmt.Errorf("release recovered lease for %s: %w", state.ContainerID, err)
	}
	if err := r.removeRuntimeState(state.ContainerID); err != nil {
		return fmt.Errorf("remove recovered state for %s: %w", state.ContainerID, err)
	}
	return nil
}

func (r Reconciler) removeRuntimeState(containerID string) error {
	removeAll := r.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	return removeAll(filepath.Join(r.RuntimeRoot, containerID))
}

func retainRecoveredState(ctx context.Context, state rootfsstate.State, processes ProcessChecker, runtime RuntimeStateChecker) (bool, error) {
	if state.FormatVersion == rootfsstate.FormatDelegationV1 && state.RootfsEnabled {
		return runtime.Running(ctx, state)
	}
	switch state.Phase {
	case rootfsstate.PhaseCreated:
		return true, nil
	case rootfsstate.PhaseAcquired, rootfsstate.PhaseMounted, rootfsstate.PhaseCreatingPending:
		return state.Wrapper.Valid() && processes.Alive(state.Wrapper), nil
	case rootfsstate.PhaseCreatingRegistered:
		return state.Delegate.Valid() && processes.Alive(state.Delegate), nil
	default:
		return false, nil
	}
}

func scanRuntimeStates(runtimeRoot string) ([]rootfsstate.State, error) {
	entries, err := os.ReadDir(runtimeRoot)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	states := make([]rootfsstate.State, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statePath := rootfsstate.Path(runtimeRoot, entry.Name())
		if _, err := os.Stat(statePath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		state, err := rootfsstate.Load(runtimeRoot, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("load runtime state %s: %w", entry.Name(), err)
		}
		if state.FormatVersion == rootfsstate.FormatFlatV1 || recoverableDelegationState(state) {
			states = append(states, state)
		}
	}
	return states, nil
}

// recoverableDelegationState 仅恢复实际创建了持久化根文件系统的委托状态。
// 沙箱和普通 runtime 的路由状态不持有 PVC 租约，不能参与 RootPV 的回收流程。
func recoverableDelegationState(state rootfsstate.State) bool {
	return state.FormatVersion == rootfsstate.FormatDelegationV1 && state.RootfsEnabled &&
		state.LeaseID != "" && state.MetaDir != "" && state.MergedDir != ""
}

// ContainerdRuntimeStateChecker 以 containerd 的 task 元数据为权威来源。
// runc 进程在 containerd 重启后可能成为孤儿；只有仍被 containerd 跟踪且处于活跃
// 状态的 task 才能继续占用 RootPV Overlay。
type ContainerdRuntimeStateChecker struct{}

func (ContainerdRuntimeStateChecker) Running(ctx context.Context, state rootfsstate.State) (bool, error) {
	namespace := state.RuntimeNamespace
	if namespace == "" {
		namespace = "k8s.io"
	}
	client, err := containerd.New("/run/containerd/containerd.sock", containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return false, fmt.Errorf("connect containerd: %w", err)
	}
	defer client.Close()
	container, err := client.LoadContainer(ctx, state.ContainerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("load containerd container: %w", err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("load containerd task: %w", err)
	}
	status, err := task.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect containerd task: %w", err)
	}
	switch status.Status {
	case containerd.Running, containerd.Created, containerd.Paused:
		return true, nil
	case containerd.Stopped:
		return false, nil
	default:
		return false, fmt.Errorf("unsupported containerd task status %q", status.Status)
	}
}

type ProcProcessChecker struct{}

func (ProcProcessChecker) Alive(identity rootfsstate.ProcessIdentity) bool {
	if !identity.Valid() {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", identity.PID))
	if err != nil {
		return false
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return false
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return false
	}
	startTime, err := strconv.ParseUint(fields[19], 10, 64)
	return err == nil && startTime == identity.StartTime
}

type FailClosedRecoveryCleaner struct{}

func (FailClosedRecoveryCleaner) Cleanup(_ context.Context, state rootfsstate.State) error {
	return fmt.Errorf("recovery_cleanup_required: phase=%s merged=%s", state.Phase, state.MergedDir)
}

type NamespaceRecoveryCleaner struct {
	ProcRoot string
	Nsenter  string
}

func (c NamespaceRecoveryCleaner) Cleanup(ctx context.Context, state rootfsstate.State) error {
	if state.MountNamespace == 0 {
		return fmt.Errorf("mount_namespace_unknown: container=%s", state.ContainerID)
	}
	procRoot := c.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	holder, err := findMountNamespaceHolder(procRoot, state.MountNamespace)
	if err != nil {
		return err
	}
	if holder == 0 {
		return nil
	}
	present, err := processMountPresent(procRoot, holder, state.MergedDir)
	if err != nil {
		return err
	}
	if present {
		nsenter := c.Nsenter
		if nsenter == "" {
			nsenter = "nsenter"
		}
		cmd := exec.CommandContext(ctx, nsenter, "-t", strconv.Itoa(holder), "-m", "--", "umount", state.MergedDir)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("normal unmount in recovered namespace: %w: %s", err, strings.TrimSpace(string(output)))
		}
		present, err = processMountPresent(procRoot, holder, state.MergedDir)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("mount_still_present: %q", state.MergedDir)
		}
	}
	if state.Phase == rootfsstate.PhaseCreatingRegistered || state.Phase == rootfsstate.PhaseDeleting || recoverableDelegationState(state) {
		if state.DelegateBinary == "" {
			return fmt.Errorf("delegate binary is unavailable for recovered cleanup")
		}
		global := runtimeGlobalArgs(state.DelegateArgs)
		deleteArgs := []string{"-t", strconv.Itoa(holder), "-m", "--", state.DelegateBinary}
		deleteArgs = append(deleteArgs, global...)
		deleteArgs = append(deleteArgs, "delete", "--force", state.ContainerID)
		nsenter := c.Nsenter
		if nsenter == "" {
			nsenter = "nsenter"
		}
		cmd := exec.CommandContext(ctx, nsenter, deleteArgs...)
		if output, err := cmd.CombinedOutput(); err != nil {
			message := strings.ToLower(string(output))
			if !strings.Contains(message, "does not exist") && !strings.Contains(message, "not found") {
				return fmt.Errorf("delete recovered runtime: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
	}
	return nil
}

func findMountNamespaceHolder(procRoot string, inode uint64) (int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		info, err := os.Stat(filepath.Join(procRoot, entry.Name(), "ns", "mnt"))
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && stat.Ino == inode {
			return pid, nil
		}
	}
	return 0, nil
}

func processMountPresent(procRoot string, pid int, target string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "mountinfo"))
	if err != nil {
		return false, err
	}
	target = filepath.Clean(target)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint, err := decodeMountInfoPath(fields[4])
		if err != nil {
			return false, err
		}
		if filepath.Clean(mountPoint) == target {
			return true, nil
		}
	}
	return false, nil
}

func runtimeGlobalArgs(args []string) []string {
	for i, arg := range args {
		if arg == "create" || arg == "run" {
			return append([]string(nil), args[:i]...)
		}
	}
	return nil
}
