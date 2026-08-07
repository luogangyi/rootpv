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

package v1alpha1

import "time"

type LockInfo struct {
	Namespace     string    `json:"namespace"`
	PodName       string    `json:"podName"`
	PodUID        string    `json:"podUID"`
	ContainerName string    `json:"containerName"`
	NodeName      string    `json:"nodeName"`
	AcquiredAt    time.Time `json:"acquiredAt"`
	HeartbeatAt   time.Time `json:"heartbeatAt"`
	HolderPID     int       `json:"holderPID"`
}

type CleanShutdownInfo struct {
	Namespace     string    `json:"namespace,omitempty"`
	PodName       string    `json:"podName,omitempty"`
	PodUID        string    `json:"podUID,omitempty"`
	ContainerName string    `json:"containerName,omitempty"`
	ContainerID   string    `json:"containerID,omitempty"`
	NodeName      string    `json:"nodeName,omitempty"`
	CleanShutdown bool      `json:"cleanShutdown"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ResolveRootfsRequest struct {
	ContainerID      string            `json:"container_id,omitempty"`
	Bundle           string            `json:"bundle,omitempty"`
	OriginalRootPath string            `json:"original_root_path,omitempty"`
	RuntimeClass     string            `json:"runtime_class,omitempty"`
	Annotations      map[string]string `json:"annotations,omitempty"`
	MountLabel       string            `json:"mount_label,omitempty"`
	SnapshotKey      string            `json:"snapshot_key"`
	ParentSnapshot   string            `json:"parent_snapshot"`
	Namespace        string            `json:"namespace"`
	PodName          string            `json:"pod_name"`
	PodUID           string            `json:"pod_uid"`
	ContainerName    string            `json:"container_name"`
	SandboxID        string            `json:"sandbox_id"`
	Image            string            `json:"image"`
	ImageID          string            `json:"image_id"`
	ChainID          string            `json:"chain_id"`
	Snapshotter      string            `json:"snapshotter,omitempty"`
	RootfsChainID    string            `json:"rootfs_chain_id,omitempty"`
	Labels           map[string]string `json:"labels"`
}

type ResolveRootfsResponse struct {
	Enabled                  bool     `json:"enabled"`
	FallbackToDefaultOverlay bool     `json:"fallback_to_default_overlay"`
	Reason                   string   `json:"reason"`
	UpperDir                 string   `json:"upper_dir"`
	WorkDir                  string   `json:"work_dir"`
	MetaDir                  string   `json:"meta_dir,omitempty"`
	MountOptions             []string `json:"mount_options,omitempty"`
	BaseImagePolicy          string   `json:"base_image_policy"`
	PersistentRootfsName     string   `json:"persistent_rootfs_name"`
	LeaseID                  string   `json:"lease_id,omitempty"`
}

type MarkMountedRequest struct {
	LeaseID          string `json:"lease_id"`
	RuntimeStatePath string `json:"runtime_state_path"`
}

type MarkMountedResponse struct {
	Mounted bool `json:"mounted"`
}

type BeginCreateRequest struct {
	LeaseID          string `json:"lease_id"`
	ContainerID      string `json:"container_id"`
	WrapperPID       int    `json:"wrapper_pid"`
	WrapperStartTime uint64 `json:"wrapper_start_time"`
}

type BeginCreateResponse struct {
	AttemptToken string `json:"attempt_token"`
}

type RegisterDelegateRequest struct {
	LeaseID           string `json:"lease_id"`
	AttemptToken      string `json:"attempt_token"`
	DelegatePID       int    `json:"delegate_pid"`
	DelegateStartTime uint64 `json:"delegate_start_time"`
}

type RegisterDelegateResponse struct {
	Registered bool `json:"registered"`
}

type CommitCreateRequest struct {
	LeaseID      string `json:"lease_id"`
	AttemptToken string `json:"attempt_token"`
}

type CommitCreateResponse struct {
	Created bool `json:"created"`
}

type ReleaseRootfsRequest struct {
	ContainerID   string            `json:"container_id,omitempty"`
	LeaseID       string            `json:"lease_id,omitempty"`
	Namespace     string            `json:"namespace,omitempty"`
	PodName       string            `json:"pod_name,omitempty"`
	PodUID        string            `json:"pod_uid,omitempty"`
	ContainerName string            `json:"container_name,omitempty"`
	MetaDir       string            `json:"meta_dir,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type ReleaseRootfsResponse struct {
	Released bool   `json:"released"`
	Reason   string `json:"reason,omitempty"`
}
