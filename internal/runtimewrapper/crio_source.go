// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package runtimewrapper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gitcode.com/openFuyao/rootpv/internal/crio"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func NewSnapshotSource(cfg Config) SnapshotSource {
	if cfg.RuntimeProvider == crio.Provider {
		return CRIOSnapshotSource{Config: cfg}
	}
	return LazyContainerdSnapshotSource{Address: cfg.ContainerdAddress, Namespace: cfg.ContainerdNamespace}
}

type CRIOSnapshotSource struct{ Config Config }

func (s CRIOSnapshotSource) storage() crio.Storage {
	return crio.Storage{Client: crio.Client{Socket: s.Config.CRIOSocket}, Helper: s.Config.CRIOStorageHelper, StateRoot: s.Config.CRIOSourceRoot}
}

func (s CRIOSnapshotSource) Resolve(ctx context.Context, id string) (result SourceOverlay, resultErr error) {
	store := s.storage()
	container, err := store.Client.Container(ctx, id)
	if err != nil {
		return result, err
	}
	owner, err := CurrentProcessIdentity()
	if err != nil {
		return result, err
	}
	source, err := store.Acquire(ctx, id, owner)
	if err != nil {
		return result, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, store.Release(context.WithoutCancel(ctx), id))
		}
	}()
	if filepath.Clean(container.Root) != filepath.Clean(source.MountPoint) {
		return result, fmt.Errorf("CRI-O daemon and storage root disagree")
	}
	result = SourceOverlay{Provider: crio.Provider, Snapshotter: "cri-o-overlay", SnapshotKey: source.LayerID, RootfsChainID: source.RootfsChainID, MountPoint: source.MountPoint, LowerDirs: source.LowerDirs, UpperDir: source.UpperDir, WorkDir: source.WorkDir}
	for _, path := range append(append([]string{}, source.LowerDirs...), source.UpperDir, source.WorkDir) {
		if err := validateSnapshotDir(path, s.Config.AllowedOverlaySnapshotRoots); err != nil {
			return result, err
		}
	}
	dir, err := store.Directory(id)
	if err != nil {
		return result, err
	}
	result.BootstrapDir = filepath.Join(dir, "bootstrap")
	if err := copyCRIOBootstrap(source.UpperDir, result.BootstrapDir); err != nil {
		return result, err
	}
	return result, nil
}

func (s CRIOSnapshotSource) Release(ctx context.Context, id string) error {
	return s.storage().Release(ctx, id)
}

func ReleaseSnapshotSource(ctx context.Context, source SnapshotSource, id string) error {
	if lifecycle, ok := source.(interface {
		Release(context.Context, string) error
	}); ok {
		return lifecycle.Release(context.WithoutCancel(ctx), id)
	}
	return nil
}

func SourceLowerDirs(source SourceOverlay) []string {
	if source.BootstrapDir == "" {
		return source.LowerDirs
	}
	return append([]string{source.BootstrapDir}, source.LowerDirs...)
}

// SetRuntimeState pins provider-specific cleanup coordinates.
func (c Config) SetRuntimeState(state *rootfsstate.State) {
	if c.RuntimeProvider != crio.Provider {
		return
	}
	state.RuntimeProvider, state.RuntimeEndpoint = crio.Provider, c.CRIOSocket
	state.CRIOStorageHelper, state.CRIOSourceRoot = c.CRIOStorageHelper, c.CRIOSourceRoot
	state.BootID = readBootID()
}

func ReleaseStateSource(ctx context.Context, state rootfsstate.State) error {
	if state.RuntimeProvider != crio.Provider {
		return nil
	}
	store := crio.Storage{Client: crio.Client{Socket: state.RuntimeEndpoint}, Helper: state.CRIOStorageHelper, StateRoot: state.CRIOSourceRoot}
	return store.Release(context.WithoutCancel(ctx), state.ContainerID)
}

func readBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return string(data)
}
