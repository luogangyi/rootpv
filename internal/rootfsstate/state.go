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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitcode.com/openFuyao/rootpv/internal/atomicfile"
)

const (
	FormatShimV1       = "rootpv-kata-shim-v1"
	FormatFlatV1       = "flat-overlay-runtime-v1"
	FormatNestedV1     = "nested-rootfs-runtime-v1"
	FormatDelegationV1 = "rootpv-delegation-v1"

	PhaseAcquired           = "acquired"
	PhaseMounted            = "mounted"
	PhaseCreatingPending    = "creating/pending"
	PhaseCreatingRegistered = "creating/registered"
	PhaseCreated            = "created"
	PhaseDeleting           = "deleting"

	AttemptPending    = "pending"
	AttemptRegistered = "registered"
	AttemptRevoked    = "revoked"
)

type ProcessIdentity struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"start_time"`
}

func (p ProcessIdentity) Valid() bool {
	return p.PID > 0 && p.StartTime > 0
}

type State struct {
	RuntimeProvider      string          `json:"runtime_provider,omitempty"`
	RuntimeEndpoint      string          `json:"runtime_endpoint,omitempty"`
	CRIOStorageHelper    string          `json:"crio_storage_helper,omitempty"`
	CRIOSourceRoot       string          `json:"crio_source_root,omitempty"`
	BootID               string          `json:"boot_id,omitempty"`
	FormatVersion        string          `json:"format_version"`
	ContainerID          string          `json:"container_id"`
	Namespace            string          `json:"namespace,omitempty"`
	PodName              string          `json:"pod_name,omitempty"`
	PodUID               string          `json:"pod_uid,omitempty"`
	ContainerName        string          `json:"container_name,omitempty"`
	LeaseID              string          `json:"lease_id,omitempty"`
	Phase                string          `json:"phase,omitempty"`
	AttemptStatus        string          `json:"attempt_status,omitempty"`
	AttemptToken         string          `json:"attempt_token,omitempty"`
	RuntimeNamespace     string          `json:"runtime_namespace,omitempty"`
	Snapshotter          string          `json:"snapshotter,omitempty"`
	SnapshotKey          string          `json:"snapshot_key,omitempty"`
	RootfsChainID        string          `json:"rootfs_chain_id,omitempty"`
	Bundle               string          `json:"bundle,omitempty"`
	OriginalRootPath     string          `json:"original_root_path,omitempty"`
	UpperDir             string          `json:"upper_dir,omitempty"`
	WorkDir              string          `json:"work_dir,omitempty"`
	MetaDir              string          `json:"meta_dir,omitempty"`
	MergedDir            string          `json:"merged_dir,omitempty"`
	SourceMountPoint     string          `json:"source_mount_point,omitempty"`
	LowerDirCount        int             `json:"lower_dir_count,omitempty"`
	Wrapper              ProcessIdentity `json:"wrapper,omitempty"`
	Delegate             ProcessIdentity `json:"delegate,omitempty"`
	Shim                 ProcessIdentity `json:"shim,omitempty"`
	MountNamespace       uint64          `json:"mount_namespace_inode,omitempty"`
	WrapperBinary        string          `json:"wrapper_binary,omitempty"`
	WrapperArgs          []string        `json:"wrapper_args,omitempty"`
	DelegateBinary       string          `json:"delegate_binary,omitempty"`
	RuntimeAdapterID     string          `json:"runtime_adapter_id,omitempty"`
	RuntimeAdapterMode   string          `json:"runtime_adapter_mode,omitempty"`
	SourceHandler        string          `json:"source_handler,omitempty"`
	RootfsHandler        string          `json:"rootpv_handler,omitempty"`
	RuntimeType          string          `json:"runtime_type,omitempty"`
	ClusterAdapterDigest string          `json:"cluster_adapter_digest,omitempty"`
	NodeResolvedDigest   string          `json:"node_resolved_digest,omitempty"`
	RootfsEnabled        bool            `json:"rootfs_enabled"`
	DelegateArgs         []string        `json:"delegate_args,omitempty"`
	DeleteArgs           []string        `json:"delete_args,omitempty"`
	CreatedAt            time.Time       `json:"created_at,omitempty"`
	UpdatedAt            time.Time       `json:"updated_at,omitempty"`
}

func Validate(state State) error {
	if state.FormatVersion != FormatShimV1 && state.FormatVersion != FormatFlatV1 && state.FormatVersion != FormatNestedV1 && state.FormatVersion != FormatDelegationV1 {
		return fmt.Errorf("unsupported runtime state format %q", state.FormatVersion)
	}
	if state.ContainerID == "" {
		return fmt.Errorf("container ID is required")
	}
	if state.FormatVersion == FormatNestedV1 {
		return nil
	}
	if state.FormatVersion == FormatDelegationV1 {
		return validateDelegationState(state)
	}
	if state.FormatVersion == FormatShimV1 {
		if state.RuntimeAdapterID == "" || state.DelegateBinary == "" || !state.Shim.Valid() || state.MountNamespace == 0 {
			return fmt.Errorf("Kata shim routing and process identity are required")
		}
	}
	return validateFlatState(state)
}

// validateDelegationState 校验 cutover 场景中恢复原始 runtime 所需的路由证据。
func validateDelegationState(state State) error {
	if state.RuntimeAdapterID == "" || state.RuntimeAdapterMode == "" || state.RootfsHandler == "" ||
		state.RuntimeType == "" || state.DelegateBinary == "" || state.ClusterAdapterDigest == "" ||
		state.NodeResolvedDigest == "" {
		return fmt.Errorf("delegation state fields are required")
	}
	if state.Phase != PhaseCreated && state.Phase != PhaseDeleting {
		return fmt.Errorf("unsupported delegation state phase %q", state.Phase)
	}
	return nil
}

// validateFlatState 校验 Flat Overlay 创建、提交和删除阶段共享的租约状态。
func validateFlatState(state State) error {
	if state.LeaseID == "" {
		return fmt.Errorf("lease ID is required")
	}
	if state.Snapshotter == "" || state.RootfsChainID == "" {
		return fmt.Errorf("snapshotter and rootfs chain ID are required")
	}
	if state.MergedDir == "" {
		return fmt.Errorf("merged directory is required")
	}
	if (state.WrapperBinary == "") != (len(state.WrapperArgs) == 0) {
		return fmt.Errorf("wrapper binary and args must be recorded together")
	}
	return validateFlatPhase(state)
}

// validateFlatPhase 按状态机阶段校验仅在该阶段必须存在的进程身份与 attempt 信息。
func validateFlatPhase(state State) error {
	switch state.Phase {
	case PhaseAcquired, PhaseMounted:
		if !state.Wrapper.Valid() {
			return fmt.Errorf("wrapper process identity is required for phase %q", state.Phase)
		}
	case PhaseCreatingPending:
		if state.AttemptStatus != AttemptPending && state.AttemptStatus != AttemptRevoked {
			return fmt.Errorf("pending or revoked attempt status is required")
		}
		if state.AttemptToken == "" {
			return fmt.Errorf("attempt token is required")
		}
		if !state.Wrapper.Valid() {
			return fmt.Errorf("wrapper process identity is required")
		}
	case PhaseCreatingRegistered:
		if state.AttemptStatus != AttemptRegistered || state.AttemptToken == "" {
			return fmt.Errorf("registered attempt token is required")
		}
		if !state.Wrapper.Valid() {
			return fmt.Errorf("wrapper process identity is required")
		}
		if !state.Delegate.Valid() {
			return fmt.Errorf("delegate process identity is required")
		}
	case PhaseCreated, PhaseDeleting:
	default:
		return fmt.Errorf("unsupported runtime state phase %q", state.Phase)
	}
	return nil
}

func Path(runtimeRoot, containerID string) string {
	return filepath.Join(runtimeRoot, containerID, "state.json")
}

func Save(runtimeRoot, containerID string, state State) error {
	state.ContainerID = containerID
	if state.FormatVersion == "" {
		state.FormatVersion = FormatNestedV1
	}
	now := time.Now().UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	// UpdatedAt 表示最近一次持久化时间，因此每次写入都刷新；CreatedAt 仅在首次写入时确定。
	state.UpdatedAt = now
	if err := Validate(state); err != nil {
		return fmt.Errorf("validate runtime state: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := Path(runtimeRoot, containerID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path+".tmp", path, data, 0o644)
}

func Load(runtimeRoot, containerID string) (State, error) {
	data, err := os.ReadFile(Path(runtimeRoot, containerID))
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode runtime state: %w", err)
	}
	if state.FormatVersion == "" {
		state.FormatVersion = FormatNestedV1
	}
	if err := Validate(state); err != nil {
		return State{}, fmt.Errorf("validate runtime state: %w", err)
	}
	return state, nil
}

func writeFileAtomic(tmp, path string, data []byte, perm os.FileMode) error {
	return atomicfile.Write(tmp, path, data, perm)
}
