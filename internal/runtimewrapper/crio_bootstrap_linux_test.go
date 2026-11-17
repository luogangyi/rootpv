//go:build linux

// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package runtimewrapper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCRIOBootstrapPreservesInitializationWithoutCopyingFiles(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "upper"), filepath.Join(root, "bootstrap")
	for _, dir := range []string{"etc", "new-working-directory"} {
		if err := os.MkdirAll(filepath.Join(source, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/proc/mounts", filepath.Join(source, "etc/mtab")); err != nil {
		t.Fatal(err)
	}
	if err := copyCRIOBootstrap(source, destination); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(destination, "etc/mtab"))
	if err != nil || target != "/proc/mounts" {
		t.Fatalf("mtab: %q, %v", target, err)
	}
	info, err := os.Stat(filepath.Join(destination, "new-working-directory"))
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("working directory: %v, %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(source, "unexpected"), []byte("do not merge into the PVC"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyCRIOBootstrap(source, filepath.Join(root, "rejected")); err == nil {
		t.Fatal("unknown mutable source file accepted")
	}
}
