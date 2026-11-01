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
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"gitcode.com/openFuyao/rootpv/internal/rootfsstate"
	"gitcode.com/openFuyao/rootpv/internal/runtimeargs"
)

func validateRunscCreate(cfg Config, spec *specs.Spec, args []string) error {
	if cfg.OverlayMode != OverlayModeFlatLowerdirs {
		return fmt.Errorf("runsc RootPV requires flat-lowerdirs")
	}
	for key := range spec.Annotations {
		if strings.HasPrefix(key, "dev.gvisor.spec.rootfs.") {
			return fmt.Errorf("runsc rootfs annotation override is unsupported: %s", key)
		}
	}
	global := runtimeGlobalArgs(args)
	root, err := runtimeargs.RunscRoot(global)
	if err != nil {
		return fmt.Errorf("validate runsc create root: %w", err)
	}
	if root == "" {
		return fmt.Errorf("runsc create requires an explicit --root")
	}
	return validateRunscOverlay(global)
}

// The native shim converts runsc_config.overlay2 into an actual CLI flag.
// An absent flag is therefore missing write-through evidence, not a safe default.
func validateRunscOverlay(global []string) error {
	overlay := ""
	needsOverlay := false
	for _, arg := range global {
		if needsOverlay {
			overlay, needsOverlay = arg, false
			continue
		}
		switch {
		case strings.HasPrefix(arg, "--overlay2=") || strings.HasPrefix(arg, "-overlay2="):
			_, overlay, _ = strings.Cut(arg, "=")
		case arg == "--overlay2" || arg == "-overlay2":
			needsOverlay = true
		case arg == "--overlay" || arg == "-overlay" || strings.HasPrefix(arg, "--overlay=") || strings.HasPrefix(arg, "-overlay="):
			return fmt.Errorf("legacy runsc overlay flag is unsupported")
		default:
			// Other vendor flags are preserved without interpretation.
		}
	}
	if needsOverlay {
		return fmt.Errorf("runsc --overlay2 requires a value")
	}
	if overlay == "" {
		return fmt.Errorf("runsc shim did not forward --overlay2=none; check the generated ConfigPath and shim configuration")
	}
	if overlay != "none" {
		return fmt.Errorf("runsc RootPV requires --overlay2=none")
	}
	return nil
}

// Close the gofer's rootfs references before attempting a normal unmount.
// CleanupPartial first checks state, so a retry after a successful delete is safe.
func (w Wrapper) cleanRunsc(ctx context.Context, delegate string, state rootfsstate.State, args, env []string) error {
	if w.RuntimeCleaner == nil {
		return fmt.Errorf("partial runtime cleaner is required")
	}
	deleteArgs, err := runscDeleteArgs(state, args)
	if err != nil {
		return err
	}
	if err := w.RuntimeCleaner.CleanupPartial(ctx, delegate, state.ContainerID, deleteArgs, env); err != nil {
		return fmt.Errorf("runsc delete before rootfs unmount: %w", err)
	}
	return nil
}

// runscDeleteArgs preserves the caller's delete flags. Only legacy states that
// lack a root may rely solely on the caller; conflicting roots fail closed.
func runscDeleteArgs(state rootfsstate.State, args []string) ([]string, error) {
	index, _ := invocationCommand(args)
	if index < 0 || args[index] != "delete" {
		return nil, fmt.Errorf("runsc cleanup requires delete arguments")
	}
	root, err := runtimeargs.RunscRoot(args[:index])
	if err != nil {
		return nil, err
	}
	savedGlobal, savedRoot, err := runtimeargs.RunscSavedGlobal(state.DelegateArgs, state.DeleteArgs)
	if err != nil {
		return nil, err
	}
	if root == "" {
		if savedRoot == "" {
			return nil, fmt.Errorf("runsc delete requires an explicit --root")
		}
		return append(savedGlobal, args...), nil
	}
	if savedRoot != "" && root != savedRoot {
		return nil, fmt.Errorf("runsc delete root differs from saved runtime root")
	}
	return append([]string(nil), args...), nil
}
