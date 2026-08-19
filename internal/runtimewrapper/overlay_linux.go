//go:build linux

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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func (SyscallOverlayMounter) MountOverlay(lower, upper, work, merged string, extraOptions []string) error {
	if lower == "" || upper == "" || work == "" || merged == "" {
		return fmt.Errorf("lower, upper, work and merged paths are required")
	}
	if err := os.MkdirAll(upper, 0o755); err != nil {
		return fmt.Errorf("create upperdir: %w", err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("create workdir: %w", err)
	}
	if err := os.MkdirAll(merged, 0o755); err != nil {
		return fmt.Errorf("create merged dir: %w", err)
	}
	options := append([]string{
		"lowerdir=" + lower,
		"upperdir=" + upper,
		"workdir=" + work,
	}, extraOptions...)
	if err := syscall.Mount("overlay", merged, "overlay", 0, strings.Join(options, ",")); err != nil {
		return fmt.Errorf("mount overlay lower=%q upper=%q work=%q merged=%q: %w", lower, upper, work, merged, err)
	}
	return nil
}

func (SyscallUnmounter) Unmount(path string) error {
	if err := syscall.Unmount(path, syscall.MNT_DETACH); err != nil && !isUnmountAlreadyClean(err) {
		return err
	}
	return nil
}

func (m *SyscallFlatOverlayMounter) Probe(upperDir string) error {
	var stat syscall.Stat_t
	if err := syscall.Stat(upperDir, &stat); err != nil {
		return fmt.Errorf("stat probe upperdir %q: %w", upperDir, err)
	}
	key := fmt.Sprintf("dev:%d", stat.Dev)
	return m.probeOnce(key, func() error {
		return probeFlatOverlayOptionsWith(mountOverlay, syscall.Unmount, filepath.Dir(upperDir))
	})
}

func (m *SyscallFlatOverlayMounter) MountFlat(req FlatOverlayMountRequest) error {
	if err := requireSameFilesystem(req.UpperDir, req.WorkDir); err != nil {
		return err
	}
	return mountFlatOverlayWith(mountOverlay, req)
}

func (SyscallNormalUnmounter) UnmountNormal(path string) error {
	return normalUnmountWith(syscall.Unmount, path)
}

// mountOverlay 是 Flat Overlay 的系统调用边界；调用方的挂载标志固定为零。
func mountOverlay(source, target, fsType, data string) error {
	return syscall.Mount(source, target, fsType, 0, data)
}

func requireSameFilesystem(upper, work string) error {
	var upperStat syscall.Stat_t
	if err := syscall.Stat(upper, &upperStat); err != nil {
		return fmt.Errorf("stat upperdir %q: %w", upper, err)
	}
	var workStat syscall.Stat_t
	if err := syscall.Stat(work, &workStat); err != nil {
		return fmt.Errorf("stat workdir %q: %w", work, err)
	}
	if upperStat.Dev != workStat.Dev {
		return fmt.Errorf("persistent_upper_work_filesystem_mismatch: upper=%q work=%q", upper, work)
	}
	return nil
}
