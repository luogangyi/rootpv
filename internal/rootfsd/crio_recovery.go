// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package rootfsd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gitcode.com/openFuyao/rootpv/internal/crio"
	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
)

func crioStorage(state rootfsstate.State) crio.Storage {
	return crio.Storage{Client: crio.Client{Socket: state.RuntimeEndpoint}, Helper: state.CRIOStorageHelper, StateRoot: state.CRIOSourceRoot}
}

func crioRuntimePresent(ctx context.Context, state rootfsstate.State) (bool, error) {
	return (crio.Client{Socket: state.RuntimeEndpoint}).Active(ctx, state.ContainerID)
}

// Keep routing until CRI-O removes its stopped record. Its later state/delete
// calls still need the pinned delegate, but no longer own the persistent root.
func (r Reconciler) finishRecoveredState(ctx context.Context, state rootfsstate.State) error {
	if state.RuntimeProvider != crio.Provider || state.FormatVersion != rootfsstate.FormatDelegationV1 {
		return r.removeRuntimeState(state.ContainerID)
	}
	_, err := (crio.Client{Socket: state.RuntimeEndpoint}).Container(ctx, state.ContainerID)
	if errors.Is(err, crio.ErrNotFound) {
		return r.removeRuntimeState(state.ContainerID)
	}
	if err != nil {
		return err
	}
	state.RootfsEnabled, state.LeaseID, state.MetaDir = false, "", ""
	state.MergedDir, state.UpperDir, state.WorkDir = "", "", ""
	state.Phase = rootfsstate.PhaseDeleting
	return rootfsstate.Save(r.RuntimeRoot, state.ContainerID, state)
}

func (r Reconciler) cleanupCRIORouting(ctx context.Context) error {
	entries, err := os.ReadDir(r.RuntimeRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		state, err := rootfsstate.Load(r.RuntimeRoot, entry.Name())
		if err != nil || state.RuntimeProvider != crio.Provider || state.RootfsEnabled || state.Phase != rootfsstate.PhaseDeleting {
			continue
		}
		_, err = (crio.Client{Socket: state.RuntimeEndpoint}).Container(ctx, state.ContainerID)
		if errors.Is(err, crio.ErrNotFound) {
			if err := os.RemoveAll(filepath.Join(r.RuntimeRoot, state.ContainerID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameCRIOBoot(state rootfsstate.State) (bool, error) {
	if state.BootID == "" {
		return false, fmt.Errorf("CRI-O recovery boot identity missing")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return string(boot) == state.BootID, err
}
