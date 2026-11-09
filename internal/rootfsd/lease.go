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
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	digest "github.com/opencontainers/go-digest"
	"golang.org/x/sys/unix"

	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

const BaseMetadataFormatV1 = "flat-overlay-lowerdirs-v1"

var errRecoveredLeaseReleased = errors.New("recovered lease is already released")

const (
	LeaseMetadataFormatV1   = "flat-overlay-lease-v1"
	LeasePhaseAcquired      = "acquired"
	LeasePhaseMounted       = "mounted"
	LeasePhaseCreating      = "creating"
	LeasePhaseCreated       = "created"
	LeasePhaseReleased      = "released"
	AttemptStatusPending    = "pending"
	AttemptStatusRegistered = "registered"
	AttemptStatusRevoked    = "revoked"

	// releasedLeaseHistoryLimit 限制进程内释放幂等记录的数量，避免长期运行的
	// rootfsd 因每个随机租约 ID 都永久保留而持续占用内存。
	releasedLeaseHistoryLimit = 1024
)

type BaseMetadata struct {
	FormatVersion   string `json:"formatVersion"`
	Snapshotter     string `json:"snapshotter"`
	RootfsChainID   string `json:"rootfsChainID"`
	BaseImagePolicy string `json:"baseImagePolicy"`
}

type LeaseAcquireRequest struct {
	RootPath        string
	ContainerID     string
	Namespace       string
	PodName         string
	PodUID          string
	ContainerName   string
	Snapshotter     string
	RootfsChainID   string
	BaseImagePolicy string
}

type Lease struct {
	LeaseID  string
	RootPath string
}

type LeaseMetadata struct {
	FormatVersion     string    `json:"formatVersion"`
	LeaseID           string    `json:"leaseID"`
	ContainerID       string    `json:"containerID"`
	Namespace         string    `json:"namespace,omitempty"`
	PodName           string    `json:"podName,omitempty"`
	PodUID            string    `json:"podUID,omitempty"`
	ContainerName     string    `json:"containerName,omitempty"`
	NodeName          string    `json:"nodeName,omitempty"`
	Phase             string    `json:"phase"`
	RuntimeStatePath  string    `json:"runtimeStatePath,omitempty"`
	AttemptStatus     string    `json:"attemptStatus,omitempty"`
	AttemptToken      string    `json:"attemptToken,omitempty"`
	WrapperPID        int       `json:"wrapperPID,omitempty"`
	WrapperStartTime  uint64    `json:"wrapperStartTime,omitempty"`
	DelegatePID       int       `json:"delegatePID,omitempty"`
	DelegateStartTime uint64    `json:"delegateStartTime,omitempty"`
	AcquiredAt        time.Time `json:"acquiredAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type heldLease struct {
	file     *os.File
	rootPath string
	metadata LeaseMetadata
}

type releasedLeaseRecord struct {
	leaseID  string
	sequence uint64
}

type LeaseManager struct {
	nodeName string
	now      func() time.Time

	mu              sync.Mutex
	held            map[string]*heldLease
	released        map[string]uint64
	releasedOrder   []releasedLeaseRecord
	releaseSequence uint64
}

// NewLeaseManager 管理进程内持有的 flock 及其磁盘元数据；flock 是同一持久写层的单写者约束。
func NewLeaseManager(nodeName string, now func() time.Time) *LeaseManager {
	if now == nil {
		now = time.Now
	}
	return &LeaseManager{
		nodeName: nodeName,
		now:      now,
		held:     map[string]*heldLease{},
		released: map[string]uint64{},
	}
}

// Acquire 创建并持有 upperdir 的非阻塞独占 flock，同时原子写入可恢复的 lease 元数据。
func (m *LeaseManager) Acquire(ctx context.Context, req LeaseAcquireRequest) (lease Lease, returnErr error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	if req.RootPath == "" || req.ContainerID == "" {
		return Lease{}, fmt.Errorf("lease request requires root path and container ID")
	}
	for _, name := range []string{"upper", "work", "meta"} {
		if err := os.MkdirAll(filepath.Join(req.RootPath, name), 0o755); err != nil {
			return Lease{}, fmt.Errorf("create rootfs %s directory: %w", name, err)
		}
	}
	lockPath := filepath.Join(req.RootPath, "meta", "lease.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Lease{}, fmt.Errorf("open lease lock: %w", err)
	}
	// 成功加入 held 后，文件描述符的关闭责任转移给 Release；其他路径由此 defer 关闭。
	defer func() {
		if file == nil {
			return
		}
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close lease lock: %w", err))
		}
	}()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return Lease{}, fmt.Errorf("persistent_upper_in_use: %w", err)
		}
		return Lease{}, fmt.Errorf("acquire rootfs flock: %w", err)
	}
	locked := true
	defer func() {
		if locked {
			if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("unlock rootfs lease: %w", err))
			}
		}
	}()
	// base.json 将持久写层绑定到确定的镜像 chain ID，阻止跨镜像错误复用。
	if err := ensureBaseMetadata(req.RootPath, req.Snapshotter, req.RootfsChainID, req.BaseImagePolicy); err != nil {
		return Lease{}, err
	}
	if err := cleanDir(filepath.Join(req.RootPath, "work")); err != nil {
		return Lease{}, fmt.Errorf("clean persistent workdir: %w", err)
	}
	leaseID, err := randomID()
	if err != nil {
		return Lease{}, fmt.Errorf("generate lease ID: %w", err)
	}
	now := m.now().UTC()
	metadata := LeaseMetadata{
		FormatVersion: LeaseMetadataFormatV1,
		LeaseID:       leaseID,
		ContainerID:   req.ContainerID,
		Namespace:     req.Namespace,
		PodName:       req.PodName,
		PodUID:        req.PodUID,
		ContainerName: req.ContainerName,
		NodeName:      m.nodeName,
		Phase:         LeasePhaseAcquired,
		AcquiredAt:    now,
		UpdatedAt:     now,
	}
	if err := writeJSONAtomic(filepath.Join(req.RootPath, "meta", "lease.json"), metadata); err != nil {
		return Lease{}, fmt.Errorf("write lease metadata: %w", err)
	}
	m.mu.Lock()
	m.held[leaseID] = &heldLease{file: file, rootPath: req.RootPath, metadata: metadata}
	m.forgetReleasedLocked(leaseID)
	m.mu.Unlock()
	locked = false
	file = nil
	return Lease{LeaseID: leaseID, RootPath: req.RootPath}, nil
}

// Recover 在守护进程重启后重新取得 lease.lock，并核验 state 与磁盘元数据的一致性。
// 返回 released=true 表示租约已完成释放，只需要由协调器删除残留 runtime state。
func (m *LeaseManager) Recover(ctx context.Context, state rootfsstate.State) (bool, error) {
	if err := validateRecoveryState(ctx, state); err != nil {
		return false, err
	}
	if m.isHeld(state.LeaseID) {
		return false, nil
	}
	lock, err := acquireRecoveryLock(state)
	if err != nil {
		return false, err
	}
	metadata, err := loadRecoveryMetadata(state)
	if err != nil {
		if errors.Is(err, errRecoveredLeaseReleased) {
			return true, finishRecovery(lock, nil)
		}
		return false, finishRecovery(lock, err)
	}
	return false, finishRecovery(lock, m.recordRecoveredLease(state, metadata, lock))
}

func finishRecovery(lock *recoveryLock, result error) error {
	if releaseErr := lock.release(); releaseErr != nil {
		return errors.Join(result, releaseErr)
	}
	return result
}

type recoveryLock struct {
	file   *os.File
	locked bool
}

func validateRecoveryState(ctx context.Context, state rootfsstate.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.LeaseID == "" || state.MetaDir == "" || state.ContainerID == "" {
		return fmt.Errorf("lease recovery state is incomplete")
	}
	return nil
}

func (m *LeaseManager) isHeld(leaseID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held[leaseID] != nil
}

func (m *LeaseManager) isReleased(leaseID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, released := m.released[leaseID]
	return released
}

func acquireRecoveryLock(state rootfsstate.State) (*recoveryLock, error) {
	file, err := os.OpenFile(filepath.Join(state.MetaDir, "lease.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open recovered lease lock: %w", err)
	}
	fileClosed := false
	defer func() {
		if fileClosed {
			return
		}
		if err := file.Close(); err != nil {
			return
		}
	}()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := file.Close()
		fileClosed = true
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("persistent_upper_in_use: recover lease %q: %w", state.LeaseID, err), closeErr)
		}
		return nil, errors.Join(fmt.Errorf("recover rootfs flock: %w", err), closeErr)
	}
	fileClosed = true
	return &recoveryLock{file: file, locked: true}, nil
}

func (l *recoveryLock) release() error {
	if l.file == nil {
		return nil
	}
	var releaseErr error
	if l.locked {
		if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); err != nil {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	if err := l.file.Close(); err != nil {
		releaseErr = errors.Join(releaseErr, err)
	}
	l.file = nil
	l.locked = false
	return releaseErr
}

func loadRecoveryMetadata(state rootfsstate.State) (LeaseMetadata, error) {
	data, err := os.ReadFile(filepath.Join(state.MetaDir, "lease.json"))
	if err != nil {
		return LeaseMetadata{}, fmt.Errorf("read recovered lease metadata: %w", err)
	}
	var metadata LeaseMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return LeaseMetadata{}, fmt.Errorf("decode recovered lease metadata: %w", err)
	}
	if metadata.FormatVersion != LeaseMetadataFormatV1 || metadata.LeaseID != state.LeaseID || metadata.ContainerID != state.ContainerID {
		return LeaseMetadata{}, fmt.Errorf("recovered lease metadata mismatch")
	}
	if metadata.Phase == LeasePhaseReleased {
		return LeaseMetadata{}, fmt.Errorf("%w: %q", errRecoveredLeaseReleased, state.LeaseID)
	}
	return metadata, nil
}

func (m *LeaseManager) recordRecoveredLease(state rootfsstate.State, metadata LeaseMetadata, lock *recoveryLock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held[state.LeaseID] != nil {
		return fmt.Errorf("lease %q is already recovered", state.LeaseID)
	}
	m.held[state.LeaseID] = &heldLease{file: lock.file, rootPath: filepath.Dir(state.MetaDir), metadata: metadata}
	m.forgetReleasedLocked(state.LeaseID)
	lock.file = nil
	lock.locked = false
	return nil
}

// MarkMounted 记录 Overlay 已挂载，后续创建只能从该阶段开始。
func (m *LeaseManager) MarkMounted(ctx context.Context, leaseID, runtimeStatePath string) error {
	return m.update(ctx, leaseID, func(lease *heldLease) error {
		if lease.metadata.Phase != LeasePhaseAcquired {
			return fmt.Errorf("lease_phase_invalid: expected acquired, got %q", lease.metadata.Phase)
		}
		if runtimeStatePath == "" {
			return fmt.Errorf("runtime state path is required")
		}
		lease.metadata.Phase = LeasePhaseMounted
		lease.metadata.RuntimeStatePath = runtimeStatePath
		return nil
	})
}

// BeginCreate 生成一次性 attempt token，并绑定 wrapper 的 PID/starttime 以防 PID 重用。
func (m *LeaseManager) BeginCreate(ctx context.Context, leaseID, containerID string, wrapper rootfsstate.ProcessIdentity) (string, error) {
	var token string
	err := m.update(ctx, leaseID, func(lease *heldLease) error {
		if lease.metadata.Phase != LeasePhaseMounted {
			return fmt.Errorf("lease_not_mounted: phase is %q", lease.metadata.Phase)
		}
		if containerID == "" || containerID != lease.metadata.ContainerID || !wrapper.Valid() {
			return fmt.Errorf("create attempt identity is invalid")
		}
		var err error
		token, err = randomID()
		if err != nil {
			return err
		}
		lease.metadata.Phase = LeasePhaseCreating
		lease.metadata.AttemptStatus = AttemptStatusPending
		lease.metadata.AttemptToken = token
		lease.metadata.WrapperPID = wrapper.PID
		lease.metadata.WrapperStartTime = wrapper.StartTime
		return nil
	})
	return token, err
}

// RegisterDelegate 仅接受当前 attempt 的首次登记；创建提交必须由已登记的 delegate 驱动。
func (m *LeaseManager) RegisterDelegate(ctx context.Context, leaseID, token string, delegate rootfsstate.ProcessIdentity) error {
	return m.update(ctx, leaseID, func(lease *heldLease) error {
		if lease.metadata.AttemptStatus == AttemptStatusRevoked {
			return fmt.Errorf("create_attempt_revoked")
		}
		if lease.metadata.Phase != LeasePhaseCreating || token == "" || token != lease.metadata.AttemptToken {
			return fmt.Errorf("create_attempt_token_invalid")
		}
		if lease.metadata.AttemptStatus != AttemptStatusPending || !delegate.Valid() {
			return fmt.Errorf("delegate registration is invalid")
		}
		lease.metadata.AttemptStatus = AttemptStatusRegistered
		lease.metadata.DelegatePID = delegate.PID
		lease.metadata.DelegateStartTime = delegate.StartTime
		return nil
	})
}

// CommitCreate 将已登记的创建尝试固化为 created，拒绝跳过身份登记的提交。
func (m *LeaseManager) CommitCreate(ctx context.Context, leaseID, token string) error {
	return m.update(ctx, leaseID, func(lease *heldLease) error {
		if lease.metadata.Phase != LeasePhaseCreating || lease.metadata.AttemptStatus != AttemptStatusRegistered || token == "" || token != lease.metadata.AttemptToken {
			return fmt.Errorf("create_attempt_token_invalid")
		}
		lease.metadata.Phase = LeasePhaseCreated
		return nil
	})
}

func (m *LeaseManager) RevokeCreate(ctx context.Context, leaseID, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lease := m.held[leaseID]
	if lease == nil {
		return fmt.Errorf("lease_not_found: %q", leaseID)
	}
	if lease.metadata.Phase != LeasePhaseCreating || token == "" || token != lease.metadata.AttemptToken {
		return fmt.Errorf("create_attempt_token_invalid")
	}
	// 清理可能在撤销状态已落盘后失败；重试时必须允许同一 token 再次撤销，
	// 才能继续清理并释放租约。其他状态仍保持 fail-closed。
	if lease.metadata.AttemptStatus == AttemptStatusRevoked {
		return nil
	}
	if lease.metadata.AttemptStatus != AttemptStatusPending {
		return fmt.Errorf("create_attempt_token_invalid")
	}
	before := lease.metadata
	lease.metadata.AttemptStatus = AttemptStatusRevoked
	lease.metadata.UpdatedAt = m.now().UTC()
	if err := writeJSONAtomic(filepath.Join(lease.rootPath, "meta", "lease.json"), lease.metadata); err != nil {
		lease.metadata = before
		return fmt.Errorf("write lease metadata: %w", err)
	}
	return nil
}

// Release 先持久化 released 状态再解锁文件；重复释放保持幂等，便于 delete/recovery 重试。
func (m *LeaseManager) Release(ctx context.Context, leaseID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.released[leaseID]; ok {
		return true, nil
	}
	lease := m.held[leaseID]
	if lease == nil {
		return false, fmt.Errorf("lease_not_found: %q", leaseID)
	}
	before := lease.metadata
	lease.metadata.Phase = LeasePhaseReleased
	lease.metadata.UpdatedAt = m.now().UTC()
	if err := writeJSONAtomic(filepath.Join(lease.rootPath, "meta", "lease.json"), lease.metadata); err != nil {
		lease.metadata = before
		return false, fmt.Errorf("write released lease metadata: %w", err)
	}
	// 即使显式解锁失败也必须关闭文件描述符。Close 会释放内核持有的 flock，
	// 并避免守护进程长期运行时泄漏 fd；两个错误一起返回以保留诊断信息。
	releaseErr := releaseLeaseFile(lease.file)
	delete(m.held, leaseID)
	m.rememberReleasedLocked(leaseID)
	if releaseErr != nil {
		return false, releaseErr
	}
	return true, nil
}

func releaseLeaseFile(file *os.File) error {
	return releaseLeaseResources(
		func() error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) },
		file.Close,
	)
}

// releaseLeaseResources 无论解锁是否报错都执行关闭操作，保证 fd 的生命周期不被
// 前一个错误截断。拆出该函数也使异常分支可以在不依赖内核 flock 故障注入的情况下测试。
func releaseLeaseResources(unlock, closeFile func() error) error {
	var releaseErr error
	if err := unlock(); err != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("unlock rootfs lease: %w", err))
	}
	if err := closeFile(); err != nil {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("close rootfs lease: %w", err))
	}
	return releaseErr
}

// rememberReleasedLocked 保存最近的释放结果以支持短期重复请求。队列项带序号，
// 因而租约 ID 极小概率重用时，旧队列项也不会误删新的幂等记录。
func (m *LeaseManager) rememberReleasedLocked(leaseID string) {
	if _, exists := m.released[leaseID]; exists {
		return
	}
	m.releaseSequence++
	record := releasedLeaseRecord{leaseID: leaseID, sequence: m.releaseSequence}
	m.released[leaseID] = record.sequence
	m.releasedOrder = append(m.releasedOrder, record)
	if len(m.releasedOrder) <= releasedLeaseHistoryLimit {
		return
	}
	expired := m.releasedOrder[0]
	m.releasedOrder = m.releasedOrder[1:]
	if m.released[expired.leaseID] == expired.sequence {
		delete(m.released, expired.leaseID)
	}
}

func (m *LeaseManager) forgetReleasedLocked(leaseID string) {
	delete(m.released, leaseID)
}

// update 在进程锁内完成状态转换，并以原子文件替换落盘；写失败时恢复内存中的旧状态。
func (m *LeaseManager) update(ctx context.Context, leaseID string, mutate func(*heldLease) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lease := m.held[leaseID]
	if lease == nil {
		return fmt.Errorf("lease_not_found: %q", leaseID)
	}
	before := lease.metadata
	if err := mutate(lease); err != nil {
		return err
	}
	lease.metadata.UpdatedAt = m.now().UTC()
	if err := writeJSONAtomic(filepath.Join(lease.rootPath, "meta", "lease.json"), lease.metadata); err != nil {
		lease.metadata = before
		return fmt.Errorf("write lease metadata: %w", err)
	}
	return nil
}

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", data[:]), nil
}

func ensureBaseMetadata(rootPath, snapshotter, rootfsChainID, policy string) error {
	if policy != "Strict" {
		return fmt.Errorf("base_policy_unsupported: %q", policy)
	}
	if snapshotter == "" {
		return fmt.Errorf("base_metadata_invalid: snapshotter is required")
	}
	if _, err := digest.Parse(rootfsChainID); err != nil {
		return fmt.Errorf("base_metadata_invalid: rootfs chain ID %q: %w", rootfsChainID, err)
	}
	upperDir := filepath.Join(rootPath, "upper")
	metaDir := filepath.Join(rootPath, "meta")
	if err := os.MkdirAll(upperDir, 0o755); err != nil {
		return fmt.Errorf("create upperdir: %w", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create metadata dir: %w", err)
	}
	path := filepath.Join(metaDir, "base.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		entries, readErr := os.ReadDir(upperDir)
		if readErr != nil {
			return fmt.Errorf("read upperdir: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("legacy_upper_without_metadata: upperdir contains %d entries", len(entries))
		}
		return writeJSONAtomic(path, BaseMetadata{
			FormatVersion:   BaseMetadataFormatV1,
			Snapshotter:     snapshotter,
			RootfsChainID:   rootfsChainID,
			BaseImagePolicy: policy,
		})
	}
	if err != nil {
		return fmt.Errorf("read base metadata: %w", err)
	}
	var base BaseMetadata
	if err := json.Unmarshal(data, &base); err != nil {
		return fmt.Errorf("base_metadata_invalid: %w", err)
	}
	if base.FormatVersion == "" || base.Snapshotter == "" || base.RootfsChainID == "" || base.BaseImagePolicy == "" {
		return fmt.Errorf("base_metadata_invalid: required field is empty")
	}
	if base.FormatVersion != BaseMetadataFormatV1 {
		return fmt.Errorf("base_metadata_version_unsupported: %q", base.FormatVersion)
	}
	if base.BaseImagePolicy != "Strict" {
		return fmt.Errorf("base_policy_unsupported: %q", base.BaseImagePolicy)
	}
	if base.Snapshotter != snapshotter || base.RootfsChainID != rootfsChainID {
		return fmt.Errorf("base_image_mismatch: stored snapshotter=%q chain=%q, requested snapshotter=%q chain=%q", base.Snapshotter, base.RootfsChainID, snapshotter, rootfsChainID)
	}
	return nil
}
