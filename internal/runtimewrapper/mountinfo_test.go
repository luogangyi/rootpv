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
	"testing"
)

func TestMountInfoCrossCheckContainerdFixtures(t *testing.T) {
	tests := []struct {
		name       string
		lowerCount int
	}{
		{name: "containerd 1.6.28 single lower", lowerCount: 1},
		{name: "containerd 2.1.1 multiple lowers", lowerCount: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := sourceOverlayFixture(t, root, tt.lowerCount)
			target := filepath.Join(root, "runtime", "rootfs")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			fixture := overlayMountInfoLine(target, source)

			got, err := InspectAndValidateSourceOverlay(strings.NewReader(fixture), target, source, []string{root})
			if err != nil {
				t.Fatalf("InspectAndValidateSourceOverlay: %v", err)
			}
			if strings.Join(got.LowerDirs, ":") != strings.Join(source.LowerDirs, ":") || got.UpperDir != source.UpperDir || got.WorkDir != source.WorkDir {
				t.Fatalf("unexpected inspected mount: got %+v want %+v", got, source)
			}
		})
	}
}

func TestMountInfoDecodesKernelEscapes(t *testing.T) {
	got, err := decodeMountInfoField(`alpha\040beta\011gamma\012delta\134omega`)
	if err != nil {
		t.Fatalf("decodeMountInfoField: %v", err)
	}
	want := "alpha beta\tgamma\ndelta\\omega"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMountInfoRequiresExactRootMountPoint(t *testing.T) {
	root := t.TempDir()
	source := sourceOverlayFixture(t, root, 1)
	mountPoint := filepath.Join(root, "runtime")
	target := filepath.Join(mountPoint, "rootfs")
	fixture := overlayMountInfoLine(mountPoint, source)

	_, err := InspectAndValidateSourceOverlay(strings.NewReader(fixture), target, source, []string{root})
	if err == nil || !strings.Contains(err.Error(), "source_mount_not_found") {
		t.Fatalf("expected exact mountpoint rejection, got %v", err)
	}
}

func TestMountInfoRejectsAPIKernelMismatch(t *testing.T) {
	root := t.TempDir()
	source := sourceOverlayFixture(t, root, 2)
	target := filepath.Join(root, "runtime", "rootfs")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	kernelSource := source
	kernelSource.LowerDirs = append([]string(nil), source.LowerDirs...)
	kernelSource.LowerDirs[0], kernelSource.LowerDirs[1] = kernelSource.LowerDirs[1], kernelSource.LowerDirs[0]

	_, err := InspectAndValidateSourceOverlay(strings.NewReader(overlayMountInfoLine(target, kernelSource)), target, source, []string{root})
	if err == nil || !strings.Contains(err.Error(), "source_mount_api_mismatch") {
		t.Fatalf("expected API mismatch, got %v", err)
	}
}

func TestMountInfoRejectsInvalidOptionsAndFilesystem(t *testing.T) {
	root := t.TempDir()
	source := sourceOverlayFixture(t, root, 1)
	target := filepath.Join(root, "runtime", "rootfs")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		fixture    string
		wantReason string
	}{
		{
			name:       "not overlay",
			fixture:    fmt.Sprintf("36 25 0:32 / %s rw - ext4 /dev/sda rw\n", mountInfoEscape(target)),
			wantReason: "source_filesystem_not_overlay",
		},
		{
			name: "missing workdir",
			fixture: fmt.Sprintf("36 25 0:32 / %s rw - overlay overlay rw,lowerdir=%s,upperdir=%s\n",
				mountInfoEscape(target), mountInfoEscape(source.LowerDirs[0]), mountInfoEscape(source.UpperDir)),
			wantReason: "source_overlay_options_invalid",
		},
		{
			name: "duplicate upperdir",
			fixture: strings.TrimSpace(overlayMountInfoLine(target, source)) +
				",upperdir=" + mountInfoEscape(filepath.Join(root, "other", "fs")) + "\n",
			wantReason: "source_overlay_options_invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InspectAndValidateSourceOverlay(strings.NewReader(tt.fixture), target, source, []string{root})
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("expected %q, got %v", tt.wantReason, err)
			}
		})
	}
}

func TestMountInfoValidatesSnapshotPathBoundaryAndState(t *testing.T) {
	assertInvalidSourceOverlay(t, "outside allowlist", "source_path_outside_allowlist", mutateLowerOutsideAllowlist)
	assertInvalidSourceOverlay(t, "symlink lower", "source_overlay_options_invalid", mutateLowerSymlink)
	assertInvalidSourceOverlay(t, "nonempty active upper", "source_active_upper_not_empty", mutateActiveUpper)
}

func assertInvalidSourceOverlay(t *testing.T, name, reason string, mutate func(*testing.T, *SourceOverlay, string)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		root := t.TempDir()
		source := sourceOverlayFixture(t, root, 1)
		mutate(t, &source, root)
		target := filepath.Join(root, "runtime", "rootfs")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := InspectAndValidateSourceOverlay(strings.NewReader(overlayMountInfoLine(target, source)), target, source, []string{root})
		if err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("expected %q, got %v", reason, err)
		}
	})
}

func mutateLowerOutsideAllowlist(t *testing.T, source *SourceOverlay, _ string) {
	outside := t.TempDir()
	path := filepath.Join(outside, "fs")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	source.LowerDirs[0] = path
}

func mutateLowerSymlink(t *testing.T, source *SourceOverlay, root string) {
	realDir, linkDir := filepath.Join(root, "real", "fs"), filepath.Join(root, "snapshots", "link", "fs")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(linkDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	source.LowerDirs[0] = linkDir
}

func mutateActiveUpper(t *testing.T, source *SourceOverlay, _ string) {
	if err := os.WriteFile(filepath.Join(source.UpperDir, "unexpected"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sourceOverlayFixture(t *testing.T, root string, lowerCount int) SourceOverlay {
	t.Helper()
	source := SourceOverlay{
		Snapshotter:   "overlayfs",
		SnapshotKey:   "container-1",
		RootfsChainID: testRootfsChainID,
	}
	for i := lowerCount; i > 0; i-- {
		path := filepath.Join(root, "snapshots", fmt.Sprintf("%d", i), "fs")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		source.LowerDirs = append(source.LowerDirs, path)
	}
	source.UpperDir = filepath.Join(root, "snapshots", "active", "fs")
	source.WorkDir = filepath.Join(root, "snapshots", "active", "work")
	for _, path := range []string{source.UpperDir, source.WorkDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return source
}

func overlayMountInfoLine(target string, source SourceOverlay) string {
	lower := make([]string, len(source.LowerDirs))
	for i, path := range source.LowerDirs {
		lower[i] = mountInfoEscape(path)
	}
	return fmt.Sprintf("36 25 0:32 / %s rw,relatime - overlay overlay rw,lowerdir=%s,upperdir=%s,workdir=%s\n",
		mountInfoEscape(target), strings.Join(lower, ":"), mountInfoEscape(source.UpperDir), mountInfoEscape(source.WorkDir))
}

func mountInfoEscape(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\134`,
		" ", `\040`,
		"\t", `\011`,
		"\n", `\012`,
	)
	return replacer.Replace(value)
}
