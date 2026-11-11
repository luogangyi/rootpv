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
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestFlatOverlayLinuxIntegration(t *testing.T) {
	root := t.TempDir()
	lowerBase := filepath.Join(root, "snapshots", "1", "fs")
	lowerTop := filepath.Join(root, "snapshots", "2", "fs")
	sourceUpper := filepath.Join(root, "snapshots", "3", "fs")
	sourceWork := filepath.Join(root, "snapshots", "3", "work")
	sourceMerged := filepath.Join(root, "source-merged")
	persistentUpper := filepath.Join(root, "pvc", "upper")
	persistentWork := filepath.Join(root, "pvc", "work")
	persistentMerged := filepath.Join(root, "persistent-merged")
	for _, path := range []string{lowerBase, lowerTop, sourceUpper, sourceWork, sourceMerged, persistentUpper, persistentWork, persistentMerged} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeIntegrationFile(t, filepath.Join(lowerBase, "copy-up.txt"), "base")
	writeIntegrationFile(t, filepath.Join(lowerBase, "delete-me.txt"), "delete")
	writeIntegrationFile(t, filepath.Join(lowerBase, "lower-parent", "existing.txt"), "existing")
	writeIntegrationFile(t, filepath.Join(lowerBase, "opaque", "old.txt"), "old")
	writeIntegrationFile(t, filepath.Join(lowerTop, "top.txt"), "top")
	// AI images commonly exceed one page of absolute lowerdir options. Keep
	// the existing copy-up/whiteout/remount checks on that same real mount path.
	lowers := longImageLowerdirs(t, root, []string{lowerTop, lowerBase})

	sourceData := strings.Join([]string{
		"lowerdir=" + strings.Join(lowers, ":"),
		"upperdir=" + sourceUpper,
		"workdir=" + sourceWork,
	}, ",")
	if err := mountOverlay("overlay", sourceMerged, "overlay", sourceData); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EINVAL) {
			t.Skipf("requires a supported OverlayFS mount environment: %v", err)
		}
		t.Fatalf("mount source overlay: %v", err)
	}
	sourceMounted := true
	defer func() {
		if sourceMounted {
			if err := syscall.Unmount(sourceMerged, 0); err != nil {
				t.Errorf("unmount source overlay: %v", err)
			}
		}
	}()

	mountInfo, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	validated, err := InspectAndValidateSourceOverlay(mountInfo, sourceMerged, SourceOverlay{
		Snapshotter: "overlayfs", SnapshotKey: "active", RootfsChainID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LowerDirs: lowers, UpperDir: sourceUpper, WorkDir: sourceWork,
	}, []string{filepath.Join(root, "snapshots")})
	if err != nil {
		t.Fatalf("inspect active source overlay: %v", err)
	}
	if err := mountInfo.Close(); err != nil {
		t.Fatalf("close mountinfo: %v", err)
	}

	mounter := &SyscallFlatOverlayMounter{}
	if err := mounter.Probe(persistentUpper); err != nil {
		t.Fatalf("probe fixed flat options: %v", err)
	}
	if err := mounter.MountFlat(FlatOverlayMountRequest{LowerDirs: validated.LowerDirs, UpperDir: persistentUpper, WorkDir: persistentWork, MergedDir: persistentMerged}); err != nil {
		t.Fatalf("mount persistent flat overlay: %v", err)
	}
	mounted := true
	defer func() {
		if mounted {
			if err := syscall.Unmount(persistentMerged, 0); err != nil {
				t.Errorf("unmount persistent flat overlay: %v", err)
			}
		}
	}()

	writeIntegrationFile(t, filepath.Join(persistentMerged, "copy-up.txt"), "persistent")
	if err := os.Remove(filepath.Join(persistentMerged, "delete-me.txt")); err != nil {
		t.Fatalf("create whiteout: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(persistentMerged, "lower-parent", "new-dir"), 0o755); err != nil {
		t.Fatalf("create directory below lower-only parent: %v", err)
	}
	writeIntegrationFile(t, filepath.Join(persistentMerged, "lower-parent", "new-dir", "new.txt"), "new")
	if err := os.RemoveAll(filepath.Join(persistentMerged, "opaque")); err != nil {
		t.Fatalf("remove lower directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(persistentMerged, "opaque"), 0o755); err != nil {
		t.Fatalf("recreate opaque directory: %v", err)
	}
	writeIntegrationFile(t, filepath.Join(persistentMerged, "opaque", "new.txt"), "new")

	if err := (SyscallNormalUnmounter{}).UnmountNormal(persistentMerged); err != nil {
		t.Fatalf("normal unmount persistent overlay: %v", err)
	}
	mounted = false
	if err := cleanTestDir(persistentWork); err != nil {
		t.Fatalf("clean workdir before remount: %v", err)
	}
	if err := mounter.MountFlat(FlatOverlayMountRequest{LowerDirs: validated.LowerDirs, UpperDir: persistentUpper, WorkDir: persistentWork, MergedDir: persistentMerged}); err != nil {
		t.Fatalf("remount persistent flat overlay: %v", err)
	}
	mounted = true

	assertIntegrationFile(t, filepath.Join(persistentMerged, "copy-up.txt"), "persistent")
	assertIntegrationFile(t, filepath.Join(persistentMerged, "lower-parent", "existing.txt"), "existing")
	assertIntegrationFile(t, filepath.Join(persistentMerged, "lower-parent", "new-dir", "new.txt"), "new")
	assertIntegrationFile(t, filepath.Join(persistentMerged, "opaque", "new.txt"), "new")
	if _, err := os.Stat(filepath.Join(persistentMerged, "delete-me.txt")); !os.IsNotExist(err) {
		t.Fatalf("whiteout did not persist, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(persistentMerged, "opaque", "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("opaque directory exposed old lower entry, stat err=%v", err)
	}
	entries, err := os.ReadDir(sourceUpper)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("containerd active source upper was modified: %v", entries)
	}
}

func longImageLowerdirs(t *testing.T, root string, lowers []string) []string {
	t.Helper()
	for id := 4; id <= 100; id++ {
		path := filepath.Join(root, "snapshots", strconv.Itoa(id), "fs")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		lowers = append(lowers, path)
	}
	return lowers
}

func writeIntegrationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertIntegrationFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s=%q want=%q", path, data, want)
	}
}
