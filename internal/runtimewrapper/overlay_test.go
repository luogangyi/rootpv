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
	"strings"
	"syscall"
	"testing"
)

func TestUnmountAlreadyCleanErrors(t *testing.T) {
	for _, err := range []error{syscall.ENOENT, syscall.EINVAL, errors.New("not clean")} {
		got := isUnmountAlreadyClean(err)
		want := err == syscall.ENOENT || err == syscall.EINVAL
		if got != want {
			t.Fatalf("isUnmountAlreadyClean(%v)=%v, want %v", err, got, want)
		}
	}
}

func TestFlatOverlayUsesFixedOptions(t *testing.T) {
	root := t.TempDir()
	lower := []string{filepath.Join(root, "lower-2"), filepath.Join(root, "lower-1")}
	upper := filepath.Join(root, "upper")
	work := filepath.Join(root, "work")
	merged := filepath.Join(root, "merged")
	for _, path := range append(append([]string{}, lower...), upper, work) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var gotSource, gotTarget, gotFSType, gotData string
	err := mountFlatOverlayWith(func(source, target, fsType, data string) error {
		gotSource, gotTarget, gotFSType, gotData = source, target, fsType, data
		return nil
	}, FlatOverlayMountRequest{LowerDirs: lower, UpperDir: upper, WorkDir: work, MergedDir: merged})
	if err != nil {
		t.Fatalf("mountFlatOverlayWith: %v", err)
	}
	if gotSource != "overlay" || gotTarget != merged || gotFSType != "overlay" {
		t.Fatalf("unexpected mount call source=%q target=%q type=%q", gotSource, gotTarget, gotFSType)
	}
	want := "lowerdir=" + strings.Join(lower, ":") + ",upperdir=" + upper + ",workdir=" + work + ",index=off,metacopy=off,redirect_dir=off"
	if gotData != want {
		t.Fatalf("mount data:\n got %q\nwant %q", gotData, want)
	}
}

func TestFlatOverlayRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	lower := filepath.Join(root, "lower")
	upper := filepath.Join(root, "upper")
	work := filepath.Join(root, "work")
	for _, path := range []string{lower, upper, work} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	noMount := func(string, string, string, string) error {
		t.Fatal("mount must not be called")
		return nil
	}
	tests := []struct {
		name         string
		lowers       []string
		extraOptions []string
		mountLabel   string
		prepare      func(t *testing.T)
		want         string
	}{
		{name: "empty lower", want: "source_overlay_options_invalid"},
		{name: "resolver options", lowers: []string{lower}, extraOptions: []string{"nodev"}, want: "mount_options_unsupported"},
		{name: "mount label", lowers: []string{lower}, mountLabel: "system_u:object_r:container_file_t:s0", want: "mount_label_unsupported"},
		{
			name:   "nonempty work",
			lowers: []string{lower},
			prepare: func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(work, "stale"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "persistent_workdir_not_empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := cleanTestDir(work); err != nil {
				t.Fatal(err)
			}
			if tt.prepare != nil {
				tt.prepare(t)
			}
			err := mountFlatOverlayWith(noMount, FlatOverlayMountRequest{LowerDirs: tt.lowers, UpperDir: upper, WorkDir: work, MergedDir: filepath.Join(root, "merged-"+strings.ReplaceAll(tt.name, " ", "-")), ResolverOptions: tt.extraOptions, MountLabel: tt.mountLabel})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
	}
}

func TestNormalUnmounterNeverUsesDetach(t *testing.T) {
	var gotFlags int
	want := syscall.EBUSY
	err := normalUnmountWith(func(_ string, flags int) error {
		gotFlags = flags
		return want
	}, "/run/rootpv/rootfs/container/merged")
	if !errors.Is(err, want) {
		t.Fatalf("expected EBUSY, got %v", err)
	}
	if gotFlags != 0 {
		t.Fatalf("normal unmount used flags %#x", gotFlags)
	}
}

func TestFlatOverlayCapabilityProbeClassifiesUnsupportedOptions(t *testing.T) {
	root := t.TempDir()
	calls := 0
	err := probeFlatOverlayOptionsWith(func(_, _, _, data string) error {
		calls++
		if !strings.Contains(data, "index=off") || !strings.Contains(data, "metacopy=off") || !strings.Contains(data, "redirect_dir=off") {
			t.Fatalf("probe dropped fixed options: %q", data)
		}
		return syscall.EINVAL
	}, func(string, int) error { return nil }, root)
	if err == nil || !strings.Contains(err.Error(), "kernel_mount_option_unsupported") {
		t.Fatalf("expected unsupported option classification, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("probe retried with reduced options: %d calls", calls)
	}
}

func TestFlatOverlayCapabilityProbeCachesOnlySuccess(t *testing.T) {
	mounter := &SyscallFlatOverlayMounter{}
	calls := 0
	probe := func() error {
		calls++
		return nil
	}
	if err := mounter.probeOnce("device-1", probe); err != nil {
		t.Fatal(err)
	}
	if err := mounter.probeOnce("device-1", probe); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("successful probe called %d times", calls)
	}

	want := syscall.EINVAL
	failingCalls := 0
	failingProbe := func() error {
		failingCalls++
		return want
	}
	for i := 0; i < 2; i++ {
		if err := mounter.probeOnce("device-2", failingProbe); !errors.Is(err, want) {
			t.Fatalf("expected failure %v, got %v", want, err)
		}
	}
	if failingCalls != 2 {
		t.Fatalf("failed probe was incorrectly cached: %d calls", failingCalls)
	}
}

func cleanTestDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
