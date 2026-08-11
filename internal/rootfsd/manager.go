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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/atomicfile"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

type ManagerOptions struct {
	NodeName         string
	Now              func() time.Time
	RegistrationWait time.Duration
	RegistrationPoll time.Duration
	MountInfoPath    string
}

type PodRootfs struct {
	Namespace    string
	PodName      string
	PodUID       string
	NodeName     string
	Containers   []string
	PVCHostPaths map[string]string
	Specs        []rootfs.ContainerRootfsSpec
}

type Manager struct {
	nodeName string
	now      func() time.Time

	registrationWait time.Duration
	registrationPoll time.Duration
	mountInfoPath    string

	mu       sync.RWMutex
	pods     map[string]PodRootfs
	podNames map[string]string
	targets  map[string]rootfs.ContainerRootfsSpec

	prepareLocksMu sync.Mutex
	prepareLocks   map[string]*rootfsPathLock
	leases         *LeaseManager
}

// rootfsPathLock 为同一持久化写层串行化准备和清理操作。users 记录已取得
// 此锁对象的调用数，只有最后一个调用离开后才能从索引中移除，避免并发调用
// 在旧锁仍被持有时创建新锁而失去互斥保护。
type rootfsPathLock struct {
	mu    sync.Mutex
	users int
}

// NewManager 创建节点本地 rootfsd 的内存索引和租约管理器；持久化事实仍存放在 PVC 元数据中。
func NewManager(opts ManagerOptions) *Manager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	registrationWait := opts.RegistrationWait
	if registrationWait == 0 {
		registrationWait = 15 * time.Second
	}
	if registrationWait < 0 {
		registrationWait = 0
	}
	registrationPoll := opts.RegistrationPoll
	if registrationPoll <= 0 {
		registrationPoll = 200 * time.Millisecond
	}
	mountInfoPath := opts.MountInfoPath
	if mountInfoPath == "" {
		mountInfoPath = "/proc/self/mountinfo"
	}
	manager := &Manager{
		nodeName:         opts.NodeName,
		now:              now,
		registrationWait: registrationWait,
		registrationPoll: registrationPoll,
		mountInfoPath:    mountInfoPath,
		pods:             map[string]PodRootfs{},
		podNames:         map[string]string{},
		targets:          map[string]rootfs.ContainerRootfsSpec{},
		prepareLocks:     map[string]*rootfsPathLock{},
	}
	manager.leases = NewLeaseManager(manager.nodeName, manager.now)
	return manager
}

// Reconcile 在 rootfsd 重启后根据 runtime state 与租约元数据恢复或清理未完成事务。
func (m *Manager) Reconcile(ctx context.Context, runtimeRoot string, cleaner RecoveryCleaner) error {
	return (Reconciler{
		RuntimeRoot: runtimeRoot,
		Leases:      m.leases,
		Processes:   ProcProcessChecker{},
		Cleaner:     cleaner,
	}).Reconcile(ctx)
}

// RegisterPod 将控制器观察到的已调度 Pod 转换为节点侧可信查找表。
// 同名 Pod UID 变化时先删除旧目标，避免新 Pod 复用旧生命周期信息。
func (m *Manager) RegisterPod(ctx context.Context, pod PodRootfs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pod.PodUID == "" {
		return fmt.Errorf("pod UID is required")
	}
	if pod.NodeName != "" && m.nodeName != "" && pod.NodeName != m.nodeName {
		return fmt.Errorf("pod %s/%s is scheduled to node %q, not %q", pod.Namespace, pod.PodName, pod.NodeName, m.nodeName)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pod.Namespace != "" && pod.PodName != "" {
		key := podNameKey(pod.Namespace, pod.PodName)
		if oldUID := m.podNames[key]; oldUID != "" && oldUID != pod.PodUID {
			m.removePodLocked(oldUID)
		}
	}
	if old, ok := m.pods[pod.PodUID]; ok {
		m.removePodLocked(old.PodUID)
	}
	m.pods[pod.PodUID] = pod
	if pod.Namespace != "" && pod.PodName != "" {
		m.podNames[podNameKey(pod.Namespace, pod.PodName)] = pod.PodUID
	}
	for _, spec := range pod.Specs {
		m.targets[targetKey(pod.PodUID, spec.ContainerName)] = spec
	}
	return nil
}

func (m *Manager) PrunePods(activeUIDs map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for podUID := range m.pods {
		if _, ok := activeUIDs[podUID]; ok {
			continue
		}
		m.removePodLocked(podUID)
	}
}

// PreparePod 预建 PVC 中的 upper/work/meta 目录和节点锁，使 runtime 创建阶段只处理挂载事务。
func (m *Manager) PreparePod(ctx context.Context, podUID string) error {
	m.mu.RLock()
	pod, ok := m.pods[podUID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("pod UID %q is not registered", podUID)
	}
	return m.preparePodSnapshot(ctx, pod, false)
}

// preparePodSnapshot 以 rootfs 路径为粒度串行化准备，防止同一 PVC 上的目录、锁文件并发覆盖。
func (m *Manager) preparePodSnapshot(ctx context.Context, pod PodRootfs, cleanWorkDir bool) error {
	for _, spec := range pod.Specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		pvcHostPath := pod.PVCHostPaths[spec.VolumeName]
		if pvcHostPath == "" {
			return fmt.Errorf("PVC host path for volume %q is not known", spec.VolumeName)
		}
		rootPath := rootfsPath(pvcHostPath, spec)
		// 先确认 kubelet 已把 PVC 挂到宿主机，再创建目录并写入节点归属证据。
		if err := m.withRootfsPathLock(rootPath, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.ensurePVCHostPathMounted(pvcHostPath); err != nil {
				return err
			}
			if err := prepareSpecDirs(pvcHostPath, pod, spec, cleanWorkDir); err != nil {
				return err
			}
			if err := writeLock(pvcHostPath, pod, spec, m.nodeName, m.now()); err != nil {
				return err
			}
			metaDir := filepath.Join(rootPath, "meta")
			cleanInfo := apiv1alpha1.CleanShutdownInfo{
				Namespace:     pod.Namespace,
				PodName:       pod.PodName,
				PodUID:        pod.PodUID,
				ContainerName: spec.ContainerName,
				NodeName:      m.nodeName,
				CleanShutdown: false,
				UpdatedAt:     m.now().UTC(),
			}
			if err := writeCleanShutdownIfNeeded(metaDir, cleanInfo, cleanWorkDir); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// ResolveRootfs 将 OCI/CRI 请求映射到已注册的 Pod target，并按模式准备根目录或获取独占租约。
// 对未启用的工作负载返回 fallback；已启用但证据缺失时返回错误，确保不会静默降级。
func (m *Manager) ResolveRootfs(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	if err := ctx.Err(); err != nil {
		return apiv1alpha1.ResolveRootfsResponse{}, err
	}
	if isSandbox(req) {
		return fallback("sandbox snapshot"), nil
	}

	identity, err := m.resolveRequestIdentity(ctx, req)
	if err != nil {
		return apiv1alpha1.ResolveRootfsResponse{}, err
	}
	if identity.podUID == "" {
		if persistentRootfsRequested(req) {
			return apiv1alpha1.ResolveRootfsResponse{}, fmt.Errorf("persistent rootfs runtime is active but pod UID is unavailable for snapshot %q", req.SnapshotKey)
		}
		return fallback("pod UID unavailable"), nil
	}

	// Pod 注册可能稍晚于 runtime 调用，lookupTarget 仅在受限时间窗口内轮询等待。
	target, err := m.lookupTarget(ctx, identity.podUID, identity.containerName)
	if err != nil {
		return apiv1alpha1.ResolveRootfsResponse{}, err
	}
	if !target.podOK {
		if persistentRootfsRequested(req) {
			return apiv1alpha1.ResolveRootfsResponse{}, fmt.Errorf("persistent rootfs pod %q is not registered", identity.podUID)
		}
		return fallback("pod not registered"), nil
	}
	if identity.containerName == "" {
		if !persistentRootfsRequested(req) {
			return fallback("container identity unavailable"), nil
		}
		return apiv1alpha1.ResolveRootfsResponse{}, fmt.Errorf("persistent rootfs runtime is active for pod %s/%s but container identity is unavailable for snapshot %q", target.pod.Namespace, target.pod.PodName, req.SnapshotKey)
	}
	if !target.targetOK {
		return fallback("container is not configured for persistent rootfs"), nil
	}
	rootPath, leaseID, err := m.prepareResolvedRootfs(ctx, req, target)
	if err != nil {
		return apiv1alpha1.ResolveRootfsResponse{}, err
	}

	return apiv1alpha1.ResolveRootfsResponse{
		Enabled:              true,
		UpperDir:             filepath.Join(rootPath, "upper"),
		WorkDir:              filepath.Join(rootPath, "work"),
		MetaDir:              filepath.Join(rootPath, "meta"),
		BaseImagePolicy:      target.spec.BaseImagePolicy,
		PersistentRootfsName: fmt.Sprintf("%s-%s", target.pod.PodName, target.spec.ContainerName),
		Reason:               "persistent rootfs resolved",
		LeaseID:              leaseID,
	}, nil
}

type requestIdentity struct {
	namespace     string
	podName       string
	podUID        string
	containerName string
}

func (m *Manager) resolveRequestIdentity(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (requestIdentity, error) {
	identity := requestIdentity{
		namespace:     firstNonEmpty(req.Namespace, rootfs.LabelValue(req.Labels, "io.kubernetes.pod.namespace", "io.kubernetes.cri.sandbox-namespace")),
		podName:       firstNonEmpty(req.PodName, rootfs.LabelValue(req.Labels, "io.kubernetes.pod.name", "io.kubernetes.cri.sandbox-name")),
		podUID:        firstNonEmpty(req.PodUID, rootfs.LabelValue(req.Labels, "io.kubernetes.pod.uid", "io.kubernetes.cri.sandbox-uid")),
		containerName: firstNonEmpty(req.ContainerName, rootfs.LabelValue(req.Labels, "io.kubernetes.container.name", "io.kubernetes.cri.container-name")),
	}
	if identity.podUID == "" && identity.namespace != "" && identity.podName != "" {
		podUID, err := m.lookupPodUIDByName(ctx, identity.namespace, identity.podName, identity.containerName)
		if err != nil {
			return requestIdentity{}, err
		}
		identity.podUID = podUID
	}
	return identity, nil
}

func (m *Manager) prepareResolvedRootfs(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest, target targetLookup) (string, string, error) {
	pvcHostPath := target.pod.PVCHostPaths[target.spec.VolumeName]
	if pvcHostPath == "" {
		return "", "", fmt.Errorf("PVC host path for volume %q is not known", target.spec.VolumeName)
	}
	rootPath := rootfsPath(pvcHostPath, target.spec)
	if req.Snapshotter == "" && req.RootfsChainID == "" {
		return rootPath, "", m.preparePodSnapshot(ctx, target.pod, true)
	}
	if req.Snapshotter == "" || req.RootfsChainID == "" {
		return "", "", fmt.Errorf("flat rootfs requires both snapshotter and rootfs chain ID")
	}
	leaseID, err := m.acquireResolvedLease(ctx, req, target, rootPath, pvcHostPath)
	return rootPath, leaseID, err
}

func (m *Manager) acquireResolvedLease(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest, target targetLookup, rootPath, pvcHostPath string) (string, error) {
	leaseID := ""
	err := m.withRootfsPathLock(rootPath, func() error {
		if err := m.ensurePVCHostPathMounted(pvcHostPath); err != nil {
			return err
		}
		if err := prepareSpecDirs(pvcHostPath, target.pod, target.spec, false); err != nil {
			return err
		}
		lease, err := m.leases.Acquire(ctx, LeaseAcquireRequest{RootPath: rootPath, ContainerID: req.ContainerID, Namespace: target.pod.Namespace, PodName: target.pod.PodName, PodUID: target.pod.PodUID, ContainerName: target.spec.ContainerName, Snapshotter: req.Snapshotter, RootfsChainID: req.RootfsChainID, BaseImagePolicy: target.spec.BaseImagePolicy})
		if err == nil {
			leaseID = lease.LeaseID
		}
		return err
	})
	return leaseID, err
}

func (m *Manager) ReleaseRootfs(ctx context.Context, req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	if err := ctx.Err(); err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	if req.LeaseID != "" {
		return m.releaseLease(ctx, req.LeaseID)
	}
	if req.MetaDir != "" {
		return m.releaseKnownMeta(req)
	}
	return m.releaseRegisteredTarget(ctx, req)
}

func (m *Manager) releaseLease(ctx context.Context, leaseID string) (apiv1alpha1.ReleaseRootfsResponse, error) {
	released, err := m.leases.Release(ctx, leaseID)
	if err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	return apiv1alpha1.ReleaseRootfsResponse{Released: released, Reason: "persistent rootfs lease released"}, nil
}

func (m *Manager) releaseKnownMeta(req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	if err := m.ensurePathInMountedPVC(req.MetaDir); err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	if err := m.withRootfsPathLock(filepath.Dir(req.MetaDir), func() error {
		if err := m.ensurePathInMountedPVC(req.MetaDir); err != nil {
			return err
		}
		return writeCleanShutdown(req.MetaDir, m.cleanShutdownForRequest(req))
	}); err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	return apiv1alpha1.ReleaseRootfsResponse{Released: true, Reason: "clean shutdown recorded"}, nil
}

func (m *Manager) releaseRegisteredTarget(ctx context.Context, req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	namespace := firstNonEmpty(req.Namespace, rootfs.LabelValue(req.Labels, "io.kubernetes.pod.namespace", "io.kubernetes.cri.sandbox-namespace"))
	podName := firstNonEmpty(req.PodName, rootfs.LabelValue(req.Labels, "io.kubernetes.pod.name", "io.kubernetes.cri.sandbox-name"))
	podUID := firstNonEmpty(req.PodUID, rootfs.LabelValue(req.Labels, "io.kubernetes.pod.uid", "io.kubernetes.cri.sandbox-uid"))
	containerName := firstNonEmpty(req.ContainerName, rootfs.LabelValue(req.Labels, "io.kubernetes.container.name", "io.kubernetes.cri.container-name"))
	if podUID == "" && namespace != "" && podName != "" {
		var err error
		podUID, err = m.lookupPodUIDByName(ctx, namespace, podName, containerName)
		if err != nil {
			return apiv1alpha1.ReleaseRootfsResponse{}, err
		}
	}
	if podUID == "" || containerName == "" {
		return apiv1alpha1.ReleaseRootfsResponse{Reason: "release identity unavailable"}, nil
	}

	target, err := m.lookupTarget(ctx, podUID, containerName)
	if err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	if !target.podOK || !target.targetOK {
		return apiv1alpha1.ReleaseRootfsResponse{Reason: "rootfs target not registered"}, nil
	}
	pvcHostPath := target.pod.PVCHostPaths[target.spec.VolumeName]
	if pvcHostPath == "" {
		return apiv1alpha1.ReleaseRootfsResponse{}, fmt.Errorf("PVC host path for volume %q is not known", target.spec.VolumeName)
	}
	rootPath := rootfsPath(pvcHostPath, target.spec)
	metaDir := filepath.Join(rootPath, "meta")
	if err := m.withRootfsPathLock(rootPath, func() error {
		if err := m.ensurePVCHostPathMounted(pvcHostPath); err != nil {
			return err
		}
		return writeCleanShutdown(metaDir, m.cleanShutdownForTarget(req, target))
	}); err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	return apiv1alpha1.ReleaseRootfsResponse{Released: true, Reason: "clean shutdown recorded"}, nil
}

func (m *Manager) cleanShutdownForRequest(req apiv1alpha1.ReleaseRootfsRequest) apiv1alpha1.CleanShutdownInfo {
	return apiv1alpha1.CleanShutdownInfo{Namespace: req.Namespace, PodName: req.PodName, PodUID: req.PodUID, ContainerName: req.ContainerName, ContainerID: req.ContainerID, NodeName: m.nodeName, CleanShutdown: true, UpdatedAt: m.now().UTC()}
}

func (m *Manager) cleanShutdownForTarget(req apiv1alpha1.ReleaseRootfsRequest, target targetLookup) apiv1alpha1.CleanShutdownInfo {
	return apiv1alpha1.CleanShutdownInfo{Namespace: target.pod.Namespace, PodName: target.pod.PodName, PodUID: target.pod.PodUID, ContainerName: target.spec.ContainerName, ContainerID: req.ContainerID, NodeName: m.nodeName, CleanShutdown: true, UpdatedAt: m.now().UTC()}
}

func (m *Manager) MarkMounted(ctx context.Context, req apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	if err := m.leases.MarkMounted(ctx, req.LeaseID, req.RuntimeStatePath); err != nil {
		return apiv1alpha1.MarkMountedResponse{}, err
	}
	return apiv1alpha1.MarkMountedResponse{Mounted: true}, nil
}

func (m *Manager) BeginCreate(ctx context.Context, req apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	token, err := m.leases.BeginCreate(ctx, req.LeaseID, req.ContainerID, rootfsstate.ProcessIdentity{
		PID:       req.WrapperPID,
		StartTime: req.WrapperStartTime,
	})
	if err != nil {
		return apiv1alpha1.BeginCreateResponse{}, err
	}
	return apiv1alpha1.BeginCreateResponse{AttemptToken: token}, nil
}

func (m *Manager) RegisterDelegate(ctx context.Context, req apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	if err := m.leases.RegisterDelegate(ctx, req.LeaseID, req.AttemptToken, rootfsstate.ProcessIdentity{
		PID:       req.DelegatePID,
		StartTime: req.DelegateStartTime,
	}); err != nil {
		return apiv1alpha1.RegisterDelegateResponse{}, err
	}
	return apiv1alpha1.RegisterDelegateResponse{Registered: true}, nil
}

func (m *Manager) CommitCreate(ctx context.Context, req apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	if err := m.leases.CommitCreate(ctx, req.LeaseID, req.AttemptToken); err != nil {
		return apiv1alpha1.CommitCreateResponse{}, err
	}
	return apiv1alpha1.CommitCreateResponse{Created: true}, nil
}

func (m *Manager) RevokeCreate(ctx context.Context, leaseID, attemptToken string) error {
	return m.leases.RevokeCreate(ctx, leaseID, attemptToken)
}

// lookupTarget 等待控制器把目标 Pod 注册到本节点。超时分支再次读取当前值，
// 覆盖定时器与注册事件恰好同时到达的边界情况。
type targetLookup struct {
	pod      PodRootfs
	podOK    bool
	spec     rootfs.ContainerRootfsSpec
	targetOK bool
}

func (m *Manager) lookupTarget(ctx context.Context, podUID, containerName string) (targetLookup, error) {
	target := m.currentTarget(podUID, containerName)
	if target.podOK || !hasCRIContainerIdentity(podUID, containerName) || m.registrationWait == 0 {
		return target, nil
	}

	timer := time.NewTimer(m.registrationWait)
	defer timer.Stop()
	ticker := time.NewTicker(m.registrationPoll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return targetLookup{}, ctx.Err()
		case <-timer.C:
			return m.currentTarget(podUID, containerName), nil
		case <-ticker.C:
			target = m.currentTarget(podUID, containerName)
			if target.podOK {
				return target, nil
			}
		}
	}
}

func (m *Manager) currentTarget(podUID, containerName string) targetLookup {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pod, podOK := m.pods[podUID]
	spec, targetOK := m.targets[targetKey(podUID, containerName)]
	return targetLookup{pod: pod, podOK: podOK, spec: spec, targetOK: targetOK}
}

func (m *Manager) lookupPodUIDByName(ctx context.Context, namespace, podName, containerName string) (string, error) {
	podUID := m.currentPodUIDByName(namespace, podName)
	if podUID != "" || !hasContainerName(containerName) || m.registrationWait == 0 {
		return podUID, nil
	}

	timer := time.NewTimer(m.registrationWait)
	defer timer.Stop()
	ticker := time.NewTicker(m.registrationPoll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return m.currentPodUIDByName(namespace, podName), nil
		case <-ticker.C:
			podUID = m.currentPodUIDByName(namespace, podName)
			if podUID != "" {
				return podUID, nil
			}
		}
	}
}

func (m *Manager) currentPodUIDByName(namespace, podName string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.podNames[podNameKey(namespace, podName)]
}

func (m *Manager) removePodLocked(podUID string) {
	pod, ok := m.pods[podUID]
	if !ok {
		return
	}
	delete(m.pods, podUID)
	if pod.Namespace != "" && pod.PodName != "" {
		key := podNameKey(pod.Namespace, pod.PodName)
		if m.podNames[key] == podUID {
			delete(m.podNames, key)
		}
	}
	for _, spec := range pod.Specs {
		delete(m.targets, targetKey(podUID, spec.ContainerName))
	}
}

func (m *Manager) withRootfsPathLock(rootPath string, fn func() error) error {
	lock := m.acquireRootfsPathLock(rootPath)
	lock.mu.Lock()
	// defer 按 LIFO 执行：先解开路径锁，再降低引用计数。这样在锁切换的
	// 瞬间到达的调用仍会复用同一个互斥锁，不会与旧操作并发执行。
	defer m.releaseRootfsPathLock(rootPath, lock)
	defer lock.mu.Unlock()
	return fn()
}

func (m *Manager) acquireRootfsPathLock(rootPath string) *rootfsPathLock {
	m.prepareLocksMu.Lock()
	defer m.prepareLocksMu.Unlock()
	lock := m.prepareLocks[rootPath]
	if lock == nil {
		lock = &rootfsPathLock{}
		m.prepareLocks[rootPath] = lock
	}
	lock.users++
	return lock
}

func (m *Manager) releaseRootfsPathLock(rootPath string, lock *rootfsPathLock) {
	m.prepareLocksMu.Lock()
	defer m.prepareLocksMu.Unlock()
	if m.prepareLocks[rootPath] != lock {
		return
	}
	lock.users--
	if lock.users == 0 {
		delete(m.prepareLocks, rootPath)
	}
}

func prepareSpecDirs(pvcHostPath string, pod PodRootfs, spec rootfs.ContainerRootfsSpec, cleanWorkDir bool) error {
	rootPath := rootfsPath(pvcHostPath, spec)
	workDir := filepath.Join(rootPath, "work")
	dirs := []string{
		filepath.Join(pvcHostPath, ".rootpv-anchor", pod.PodName, spec.ContainerName),
		filepath.Join(rootPath, "upper"),
		workDir,
		filepath.Join(rootPath, "meta"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if !cleanWorkDir {
		return nil
	}
	if err := cleanDir(workDir); err != nil {
		return fmt.Errorf("clean workdir %s: %w", workDir, err)
	}
	return nil
}

func writeLock(pvcHostPath string, pod PodRootfs, spec rootfs.ContainerRootfsSpec, nodeName string, now time.Time) error {
	lock := apiv1alpha1.LockInfo{
		Namespace:     pod.Namespace,
		PodName:       pod.PodName,
		PodUID:        pod.PodUID,
		ContainerName: spec.ContainerName,
		NodeName:      nodeName,
		AcquiredAt:    now.UTC(),
		HeartbeatAt:   now.UTC(),
		HolderPID:     os.Getpid(),
	}
	metaDir := filepath.Join(rootfsPath(pvcHostPath, spec), "meta")
	return writeJSONAtomic(filepath.Join(metaDir, "lock.json"), lock)
}

func writeCleanShutdown(metaDir string, info apiv1alpha1.CleanShutdownInfo) error {
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(metaDir, "clean-shutdown.json"), info)
}

func writeCleanShutdownIfNeeded(metaDir string, info apiv1alpha1.CleanShutdownInfo, overwrite bool) error {
	if overwrite {
		return writeCleanShutdown(metaDir, info)
	}
	path := filepath.Join(metaDir, "clean-shutdown.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeCleanShutdown(metaDir, info)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileAtomic(tmp, path, data, 0o644); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(tmp, path string, data []byte, perm os.FileMode) error {
	return atomicfile.Write(tmp, path, data, perm)
}

func rootfsPath(pvcHostPath string, spec rootfs.ContainerRootfsSpec) string {
	return filepath.Join(pvcHostPath, filepath.FromSlash(spec.Path))
}

func cleanDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ensurePVCHostPathMounted(path string) error {
	if !isKubeletPVCPath(path) {
		return nil
	}
	mounted, err := mountedPVCPath(m.mountInfoPath, path, true)
	if err != nil {
		return err
	}
	if !mounted {
		return fmt.Errorf("PVC host path is not mounted: %s", path)
	}
	return nil
}

func (m *Manager) ensurePathInMountedPVC(path string) error {
	if !isKubeletPVCPath(path) {
		return nil
	}
	mounted, err := mountedPVCPath(m.mountInfoPath, path, false)
	if err != nil {
		return err
	}
	if !mounted {
		return fmt.Errorf("PVC host path is not mounted: %s", path)
	}
	return nil
}

func mountedPVCPath(mountInfoPath, path string, exact bool) (bool, error) {
	file, err := os.Open(mountInfoPath)
	if err != nil {
		return false, fmt.Errorf("open mountinfo: %w", err)
	}
	defer file.Close()

	target := filepath.Clean(path)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		mountPoint, err := decodeMountInfoPath(fields[4])
		if err != nil {
			return false, err
		}
		mountPoint = filepath.Clean(mountPoint)
		if !isKubeletPVCPath(mountPoint) {
			continue
		}
		if mountPoint == target || (!exact && pathWithinMount(target, mountPoint)) {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read mountinfo: %w", err)
	}
	return false, nil
}

func isKubeletPVCPath(path string) bool {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	var twoBack, previous string
	for _, part := range parts {
		if twoBack == "pods" && part == "volumes" {
			return true
		}
		twoBack, previous = previous, part
	}
	return false
}

func pathWithinMount(path, mountPoint string) bool {
	return strings.HasPrefix(path, mountPoint+string(filepath.Separator))
}

func decodeMountInfoPath(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+3 >= len(value) {
			return "", fmt.Errorf("invalid mountinfo escape")
		}
		decoded, err := strconv.ParseUint(value[i+1:i+4], 8, 8)
		if err != nil {
			return "", err
		}
		out.WriteByte(byte(decoded))
		i += 4
	}
	return out.String(), nil
}

func isSandbox(req apiv1alpha1.ResolveRootfsRequest) bool {
	if req.ContainerName == "POD" {
		return true
	}
	if req.Labels == nil {
		return false
	}
	return rootfs.LabelValue(req.Labels, "io.kubernetes.container.type", "io.kubernetes.cri.container-type") == "sandbox" ||
		rootfs.LabelValue(req.Labels, "io.cri-containerd.kind") == "sandbox" ||
		strings.EqualFold(rootfs.LabelValue(req.Labels, "container-type"), "sandbox")
}

func persistentRootfsRequested(req apiv1alpha1.ResolveRootfsRequest) bool {
	return rootfs.LabelValue(req.Labels, rootfs.AnnotationEnabled, rootfs.SnapshotLabelEnabled) == "true"
}

func hasCRIContainerIdentity(podUID, containerName string) bool {
	return podUID != "" && hasContainerName(containerName)
}

func hasContainerName(containerName string) bool {
	return containerName != "" && containerName != "POD"
}

func fallback(reason string) apiv1alpha1.ResolveRootfsResponse {
	return apiv1alpha1.ResolveRootfsResponse{
		FallbackToDefaultOverlay: true,
		Reason:                   reason,
	}
}

func targetKey(podUID, containerName string) string {
	return podUID + "/" + containerName
}

func podNameKey(namespace, podName string) string {
	return namespace + "/" + podName
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func DiscoverPVCHostPath(kubeletRoot, podUID, volumeName, pvcName string) (string, error) {
	podVolumeRoot := filepath.Join(kubeletRoot, "pods", podUID, "volumes")
	pluginDirs, err := os.ReadDir(podVolumeRoot)
	if err != nil {
		return "", fmt.Errorf("read pod volume root %s: %w", podVolumeRoot, err)
	}
	var candidates []string
	var fallbackCandidates []string
	for _, pluginDir := range pluginDirs {
		matched, fallback := discoverPVCInPlugin(podVolumeRoot, pluginDir, volumeName, pvcName)
		candidates = append(candidates, matched...)
		fallbackCandidates = append(fallbackCandidates, fallback...)
	}
	if len(candidates) == 0 {
		if len(fallbackCandidates) == 1 {
			return fallbackCandidates[0], nil
		}
		if len(fallbackCandidates) > 1 {
			return "", fmt.Errorf("found %d PVC volume candidates but could not uniquely match volume %q pvc %q", len(fallbackCandidates), volumeName, pvcName)
		}
		return "", errors.New("PVC host path is not mounted yet")
	}
	return candidates[0], nil
}

func discoverPVCInPlugin(podVolumeRoot string, pluginDir os.DirEntry, volumeName, pvcName string) ([]string, []string) {
	if !pluginDir.IsDir() {
		return nil, nil
	}
	pluginPath := filepath.Join(podVolumeRoot, pluginDir.Name())
	volumeDirs, err := os.ReadDir(pluginPath)
	if err != nil {
		return nil, nil
	}
	var matched, fallback []string
	for _, volumeDir := range volumeDirs {
		if !volumeDir.IsDir() {
			continue
		}
		path := filepath.Join(pluginPath, volumeDir.Name())
		mountPath, mounted := pvcMountPath(path)
		if !mounted {
			continue
		}
		if isLikelyPVCPlugin(pluginDir.Name()) {
			fallback = append(fallback, mountPath)
		}
		if matchesVolumeName(volumeDir.Name(), volumeName, pvcName) {
			matched = append(matched, mountPath)
		}
	}
	return matched, fallback
}

func pvcMountPath(path string) (string, bool) {
	mountPath := filepath.Join(path, "mount")
	if isDirectory(mountPath) {
		return mountPath, true
	}
	if isDirectory(path) {
		return path, true
	}
	return "", false
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isLikelyPVCPlugin(pluginName string) bool {
	return strings.Contains(pluginName, "kubernetes.io~csi") ||
		strings.Contains(pluginName, "kubernetes.io~local-volume")
}

func matchesVolumeName(name, volumeName, pvcName string) bool {
	if name == volumeName || name == pvcName {
		return true
	}
	return pvcName != "" && strings.Contains(name, pvcName)
}
