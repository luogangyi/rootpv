//go:build !linux

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

import "fmt"

func (SyscallOverlayMounter) MountOverlay(_, _, _, _ string, _ []string) error {
	return fmt.Errorf("overlay mount is only supported on linux")
}

func (SyscallUnmounter) Unmount(_ string) error {
	return nil
}

func (*SyscallFlatOverlayMounter) Probe(_ string) error {
	return fmt.Errorf("flat overlay mount is only supported on linux")
}

func (*SyscallFlatOverlayMounter) MountFlat(FlatOverlayMountRequest) error {
	return fmt.Errorf("flat overlay mount is only supported on linux")
}

func (SyscallNormalUnmounter) UnmountNormal(_ string) error {
	return fmt.Errorf("normal unmount is only supported on linux")
}
