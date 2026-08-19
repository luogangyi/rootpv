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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type OverlayMounter interface {
	MountOverlay(lower, upper, work, merged string, extraOptions []string) error
}

type Unmounter interface {
	Unmount(path string) error
}

type FlatOverlayMounter interface {
	Probe(upperDir string) error
	MountFlat(FlatOverlayMountRequest) error
}

// FlatOverlayMountRequest 将 Flat Overlay 挂载所需的路径和安全证据集中传递，
// 防止参数顺序错误，并使不同平台的挂载实现保持一致。
type FlatOverlayMountRequest struct {
	LowerDirs       []string
	UpperDir        string
	WorkDir         string
	MergedDir       string
	ResolverOptions []string
	MountLabel      string
}

type NormalUnmounter interface {
	UnmountNormal(path string) error
}

// mountCall 只承载本项目需要的 Overlay 挂载参数。Flat Overlay 固定使用零挂载标志，
// 因而无需把底层系统调用的类型细节暴露到业务逻辑和测试中。
type mountCall func(source, target, fsType, data string) error
type unmountCall func(path string, flags int) error

type SyscallOverlayMounter struct{}
type SyscallUnmounter struct{}
type SyscallFlatOverlayMounter struct {
	probeMu sync.Mutex
	probed  map[string]struct{}
}
type SyscallNormalUnmounter struct{}

func isUnmountAlreadyClean(err error) bool {
	return err == nil || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EINVAL)
}

func (m *SyscallFlatOverlayMounter) probeOnce(key string, probe func() error) error {
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	if _, ok := m.probed[key]; ok {
		return nil
	}
	if err := probe(); err != nil {
		return err
	}
	if m.probed == nil {
		m.probed = map[string]struct{}{}
	}
	m.probed[key] = struct{}{}
	return nil
}

func mountFlatOverlayWith(call mountCall, req FlatOverlayMountRequest) error {
	if len(req.LowerDirs) == 0 {
		return fmt.Errorf("source_overlay_options_invalid: at least one lowerdir is required")
	}
	if len(req.ResolverOptions) != 0 {
		return fmt.Errorf("mount_options_unsupported: rootfsd returned %d options", len(req.ResolverOptions))
	}
	if req.MountLabel != "" {
		return fmt.Errorf("mount_label_unsupported: SELinux mount label is nonempty")
	}
	paths := append(append([]string{}, req.LowerDirs...), req.UpperDir, req.WorkDir, req.MergedDir)
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, ",\x00") {
			return fmt.Errorf("source_overlay_options_invalid: unsafe overlay path %q", path)
		}
	}
	for _, path := range req.LowerDirs {
		if strings.Contains(path, ":") {
			return fmt.Errorf("source_overlay_options_invalid: lowerdir contains unsupported colon: %q", path)
		}
	}
	entries, err := os.ReadDir(req.WorkDir)
	if err != nil {
		return fmt.Errorf("persistent_workdir_invalid: read %q: %w", req.WorkDir, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("persistent_workdir_not_empty: %q contains %d entries", req.WorkDir, len(entries))
	}
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		return fmt.Errorf("create merged dir: %w", err)
	}
	data := strings.Join([]string{
		"lowerdir=" + strings.Join(req.LowerDirs, ":"),
		"upperdir=" + req.UpperDir,
		"workdir=" + req.WorkDir,
		"index=off",
		"metacopy=off",
		"redirect_dir=off",
	}, ",")
	if err := call("overlay", req.MergedDir, "overlay", data); err != nil {
		return fmt.Errorf("flat_overlay_mount_failed: lower=%q upper=%q work=%q merged=%q: %w", strings.Join(req.LowerDirs, ":"), req.UpperDir, req.WorkDir, req.MergedDir, err)
	}
	return nil
}

func normalUnmountWith(call unmountCall, path string) error {
	err := call(path, 0)
	if isUnmountAlreadyClean(err) {
		return nil
	}
	return err
}

func probeFlatOverlayOptionsWith(mount mountCall, unmount unmountCall, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create probe root: %w", err)
	}
	probeRoot, err := os.MkdirTemp(root, ".rootpv-flat-overlay-probe-")
	if err != nil {
		return fmt.Errorf("create probe directory: %w", err)
	}
	defer os.RemoveAll(probeRoot)
	lower := filepath.Join(probeRoot, "lower")
	upper := filepath.Join(probeRoot, "upper")
	work := filepath.Join(probeRoot, "work")
	merged := filepath.Join(probeRoot, "merged")
	for _, path := range []string{lower, upper, work, merged} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create probe path: %w", err)
		}
	}
	if err := mountFlatOverlayWith(mount, FlatOverlayMountRequest{LowerDirs: []string{lower}, UpperDir: upper, WorkDir: work, MergedDir: merged}); err != nil {
		if errors.Is(err, syscall.EINVAL) {
			return fmt.Errorf("kernel_mount_option_unsupported: %w", err)
		}
		return err
	}
	if err := normalUnmountWith(unmount, merged); err != nil {
		return fmt.Errorf("unmount flat overlay capability probe: %w", err)
	}
	return nil
}
