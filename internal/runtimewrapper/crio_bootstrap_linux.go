//go:build linux

// Copyright (c) 2026 OpenFuyao Contributors.
// SPDX-License-Identifier: MulanPSL-2.0

package runtimewrapper

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// CRI-O prepares directories and /etc/mtab before OCI create. Preserve them in
// a private immutable lower, below the PVC's existing changes and whiteouts.
// Unknown files/overlay metadata fail closed rather than overwrite PVC data.
func copyCRIOBootstrap(source, destination string) error {
	count := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 4096 {
			return fmt.Errorf("crio_source_upper_unsupported: too many entries")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return copyCRIOBootstrapEntry(path, filepath.Join(destination, rel), rel)
	})
	if err != nil {
		return fmt.Errorf("crio_source_upper_unsupported: %w", err)
	}
	return nil
}

func copyCRIOBootstrapEntry(source, destination, relative string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(source, &stat); err != nil {
		return err
	}
	opaque, err := crioBootstrapXattrs(source)
	if err != nil {
		return err
	}
	mode := stat.Mode & unix.S_IFMT
	switch mode {
	case unix.S_IFDIR:
		return copyCRIOBootstrapDirectory(destination, stat, opaque)
	case unix.S_IFLNK:
		return copyCRIOBootstrapSymlink(source, destination, relative, stat)
	default:
		return fmt.Errorf("unknown initialization file type")
	}
}

func copyCRIOBootstrapDirectory(destination string, stat unix.Stat_t, opaque []byte) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	if err := os.Chown(destination, int(stat.Uid), int(stat.Gid)); err != nil {
		return err
	}
	const permissions = unix.S_IRWXU | unix.S_IRWXG | unix.S_IRWXO | unix.S_ISUID | unix.S_ISGID | unix.S_ISVTX
	if err := unix.Chmod(destination, stat.Mode&permissions); err != nil {
		return err
	}
	if len(opaque) != 0 {
		return unix.Setxattr(destination, "trusted.overlay.opaque", opaque, 0)
	}
	return nil
}

func copyCRIOBootstrapSymlink(source, destination, relative string, stat unix.Stat_t) error {
	target, err := os.Readlink(source)
	if err != nil {
		return err
	}
	if relative != "etc/mtab" || (target != "/proc/mounts" && target != "/proc/self/mounts" && target != "../proc/self/mounts") {
		return fmt.Errorf("unknown initialization symlink")
	}
	if err := os.Symlink(target, destination); err != nil {
		return err
	}
	return os.Lchown(destination, int(stat.Uid), int(stat.Gid))
}

// origin/impure refer to the original overlay instance; they are not data.
// An opaque directory does affect lower lookup, so preserve its y/x marker.
func crioBootstrapXattrs(path string) ([]byte, error) {
	names, err := listCRIOBootstrapXattrs(path)
	if err != nil {
		return nil, err
	}
	var opaque []byte
	for _, name := range names {
		switch name {
		case "", "trusted.overlay.origin", "trusted.overlay.impure":
		case "trusted.overlay.opaque":
			opaque, err = readCRIOBootstrapOpaque(path)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported initialization xattr %q", name)
		}
	}
	return opaque, nil
}

func listCRIOBootstrapXattrs(path string) ([]string, error) {
	size, err := unix.Llistxattr(path, nil)
	if err != nil {
		return nil, err
	}
	attrs := make([]byte, size)
	n, err := unix.Llistxattr(path, attrs)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(attrs[:n]), "\x00"), nil
}

func readCRIOBootstrapOpaque(path string) ([]byte, error) {
	value := make([]byte, 2)
	n, err := unix.Lgetxattr(path, "trusted.overlay.opaque", value)
	if err != nil || n != 1 || (value[0] != 'y' && value[0] != 'x') {
		return nil, fmt.Errorf("unsupported opaque directory marker")
	}
	return value[:n], nil
}
