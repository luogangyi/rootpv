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
	"fmt"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	digest "github.com/opencontainers/go-digest"
)

type SourceOverlay struct {
	Snapshotter   string
	SnapshotKey   string
	RootfsChainID string
	MountPoint    string
	LowerDirs     []string
	UpperDir      string
	WorkDir       string
}

// overlayMountPaths 是 containerd 返回的单条 overlay 挂载所包含的三组路径。
// 使用结构体避免解析函数通过多个返回值传递彼此关联的挂载参数。
type overlayMountPaths struct {
	LowerDirs []string
	UpperDir  string
	WorkDir   string
}

type SnapshotSource interface {
	Resolve(context.Context, string) (SourceOverlay, error)
}

type SnapshotMetadataClient interface {
	ContainerSnapshot(ctx context.Context, namespace, containerID string) (snapshotter, snapshotKey string, err error)
	SnapshotInfo(ctx context.Context, namespace, snapshotter, snapshotKey string) (snapshots.Info, []mount.Mount, error)
}

type ContainerdSnapshotSource struct {
	Client    SnapshotMetadataClient
	Namespace string
}

type LazyContainerdSnapshotSource struct {
	Address   string
	Namespace string
}

// Resolve 为一次请求短暂连接 containerd，读取容器实际使用的快照而非信任 OCI 输入参数。
func (s LazyContainerdSnapshotSource) Resolve(ctx context.Context, containerID string) (SourceOverlay, error) {
	source, closeClient, err := NewContainerdSnapshotSource(s.Address, s.Namespace)
	if err != nil {
		return SourceOverlay{}, err
	}
	defer closeClient()
	return source.Resolve(ctx, containerID)
}

func NewContainerdSnapshotSource(address, namespace string) (*ContainerdSnapshotSource, func() error, error) {
	client, err := containerd.New(address)
	if err != nil {
		return nil, nil, fmt.Errorf("connect containerd: %w", err)
	}
	return &ContainerdSnapshotSource{
		Client:    &containerdSnapshotMetadataClient{client: client},
		Namespace: namespace,
	}, client.Close, nil
}

// Resolve 提取 active overlayfs 快照的 lowerdir、upperdir、workdir 与父 chain ID，
// 这些信息随后会与 mountinfo 交叉校验，作为 Flat Overlay 的只读下层证据。
func (s ContainerdSnapshotSource) Resolve(ctx context.Context, containerID string) (SourceOverlay, error) {
	if s.Client == nil {
		return SourceOverlay{}, fmt.Errorf("snapshot_identity_missing: containerd metadata client is required")
	}
	if containerID == "" {
		return SourceOverlay{}, fmt.Errorf("snapshot_identity_missing: container ID is required")
	}
	snapshotter, snapshotKey, err := s.Client.ContainerSnapshot(ctx, s.Namespace, containerID)
	if err != nil {
		return SourceOverlay{}, fmt.Errorf("load container snapshot metadata: %w", err)
	}
	if snapshotter != "overlayfs" {
		return SourceOverlay{}, fmt.Errorf("snapshotter_unsupported: got %q", snapshotter)
	}
	if snapshotKey == "" {
		return SourceOverlay{}, fmt.Errorf("snapshot_identity_missing: snapshot key is empty")
	}
	info, mounts, err := s.Client.SnapshotInfo(ctx, s.Namespace, snapshotter, snapshotKey)
	if err != nil {
		return SourceOverlay{}, fmt.Errorf("load active snapshot: %w", err)
	}
	if info.Kind != snapshots.KindActive {
		return SourceOverlay{}, fmt.Errorf("snapshot_not_active: snapshot %q has kind %s", snapshotKey, info.Kind)
	}
	if _, err := digest.Parse(info.Parent); err != nil {
		return SourceOverlay{}, fmt.Errorf("snapshot_parent_invalid: %q: %w", info.Parent, err)
	}
	paths, err := parseOverlayMounts(mounts)
	if err != nil {
		return SourceOverlay{}, err
	}
	return SourceOverlay{
		Snapshotter:   snapshotter,
		SnapshotKey:   snapshotKey,
		RootfsChainID: info.Parent,
		LowerDirs:     paths.LowerDirs,
		UpperDir:      paths.UpperDir,
		WorkDir:       paths.WorkDir,
	}, nil
}

// parseOverlayMounts 严格接受一个完整的 overlay 挂载描述，并规范化所有绝对路径。
func parseOverlayMounts(mounts []mount.Mount) (overlayMountPaths, error) {
	if len(mounts) != 1 {
		return overlayMountPaths{}, fmt.Errorf("source_overlay_options_invalid: expected one mount, got %d", len(mounts))
	}
	m := mounts[0]
	if m.Type != "overlay" {
		return overlayMountPaths{}, fmt.Errorf("source_filesystem_not_overlay: got %q", m.Type)
	}
	values := map[string]string{}
	for _, option := range m.Options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || (key != "lowerdir" && key != "upperdir" && key != "workdir") {
			continue
		}
		if _, exists := values[key]; exists {
			return overlayMountPaths{}, fmt.Errorf("source_overlay_options_invalid: duplicate %s", key)
		}
		values[key] = value
	}
	for _, key := range []string{"lowerdir", "upperdir", "workdir"} {
		if values[key] == "" {
			return overlayMountPaths{}, fmt.Errorf("source_overlay_options_invalid: missing %s", key)
		}
	}
	lower := strings.Split(values["lowerdir"], ":")
	for i, path := range append(append([]string{}, lower...), values["upperdir"], values["workdir"]) {
		if path == "" || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
			return overlayMountPaths{}, fmt.Errorf("source_overlay_options_invalid: path %q is not an absolute safe path", path)
		}
		clean := filepath.Clean(path)
		if i < len(lower) {
			lower[i] = clean
		}
	}
	return overlayMountPaths{LowerDirs: lower, UpperDir: filepath.Clean(values["upperdir"]), WorkDir: filepath.Clean(values["workdir"])}, nil
}

type containerdSnapshotMetadataClient struct {
	client *containerd.Client
}

func (c *containerdSnapshotMetadataClient) ContainerSnapshot(ctx context.Context, namespace, containerID string) (string, string, error) {
	ctx = namespaces.WithNamespace(ctx, namespace)
	container, err := c.client.LoadContainer(ctx, containerID)
	if err != nil {
		return "", "", err
	}
	info, err := container.Info(ctx)
	if err != nil {
		return "", "", err
	}
	return info.Snapshotter, info.SnapshotKey, nil
}

func (c *containerdSnapshotMetadataClient) SnapshotInfo(ctx context.Context, namespace, snapshotter, snapshotKey string) (snapshots.Info, []mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, namespace)
	service := c.client.SnapshotService(snapshotter)
	info, err := service.Stat(ctx, snapshotKey)
	if err != nil {
		return snapshots.Info{}, nil, err
	}
	mounts, err := service.Mounts(ctx, snapshotKey)
	if err != nil {
		return snapshots.Info{}, nil, err
	}
	return info, mounts, nil
}
