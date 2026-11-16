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
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxMountInfoLine = 4 << 20

type UnmountVerifier interface {
	ConfirmUnmounted(string) error
}

type ProcUnmountVerifier struct {
	MountInfoPath string
}

func (v ProcUnmountVerifier) ConfirmUnmounted(target string) error {
	path := v.MountInfoPath
	if path == "" {
		path = "/proc/self/mountinfo"
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open mountinfo: %w", err)
	}
	defer file.Close()
	target = filepath.Clean(target)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxMountInfoLine)
	for scanner.Scan() {
		line := scanner.Text()
		separator := strings.Index(line, " - ")
		if separator < 0 {
			continue
		}
		fields := strings.Fields(line[:separator])
		if len(fields) < 5 {
			continue
		}
		mountPoint, err := decodeMountInfoField(fields[4])
		if err != nil {
			return err
		}
		if filepath.Clean(mountPoint) == target {
			return fmt.Errorf("mount_still_present: %q", target)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}
	return nil
}

func InspectAndValidateSourceOverlay(r io.Reader, target string, api SourceOverlay, allowedRoots []string) (SourceOverlay, error) {
	kernel, err := inspectOverlayMount(r, target)
	if err != nil {
		return SourceOverlay{}, err
	}
	if api.Provider == "cri-o" {
		for i, path := range kernel.LowerDirs {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return SourceOverlay{}, err
			}
			kernel.LowerDirs[i] = resolved
		}
	}
	if !equalPaths(kernel.LowerDirs, api.LowerDirs) || filepath.Clean(kernel.UpperDir) != filepath.Clean(api.UpperDir) || filepath.Clean(kernel.WorkDir) != filepath.Clean(api.WorkDir) {
		return SourceOverlay{}, fmt.Errorf("source_mount_api_mismatch: kernel lower=%v upper=%q work=%q, API lower=%v upper=%q work=%q",
			kernel.LowerDirs, kernel.UpperDir, kernel.WorkDir, api.LowerDirs, api.UpperDir, api.WorkDir)
	}
	if err := ValidateSourceOverlay(api, allowedRoots); err != nil {
		return SourceOverlay{}, err
	}
	api.MountPoint = filepath.Clean(target)
	return api, nil
}

// ValidateSourceOverlay validates trusted containerd snapshot paths before a shim mounts them.
func ValidateSourceOverlay(api SourceOverlay, allowedRoots []string) error {
	if len(api.LowerDirs) == 0 {
		return fmt.Errorf("source_filesystem_not_overlay")
	}
	for _, path := range append(append([]string{}, api.LowerDirs...), api.UpperDir, api.WorkDir) {
		if err := validateSnapshotDir(path, allowedRoots); err != nil {
			return err
		}
	}
	if api.Provider == "cri-o" {
		if api.BootstrapDir == "" {
			return fmt.Errorf("CRI-O bootstrap layer is required")
		}
		return nil
	}
	entries, err := os.ReadDir(api.UpperDir)
	if err != nil {
		return fmt.Errorf("source_overlay_options_invalid: read active upper %q: %w", api.UpperDir, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("source_active_upper_not_empty: %q contains %d entries", api.UpperDir, len(entries))
	}
	return nil
}

func inspectOverlayMount(r io.Reader, target string) (SourceOverlay, error) {
	target = filepath.Clean(target)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxMountInfoLine)
	var selected string
	for scanner.Scan() {
		line := scanner.Text()
		separator := strings.Index(line, " - ")
		if separator < 0 {
			continue
		}
		left := strings.Fields(line[:separator])
		if len(left) < 6 {
			continue
		}
		mountPoint, err := decodeMountInfoField(left[4])
		if err != nil {
			return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: decode mountpoint: %w", err)
		}
		if filepath.Clean(mountPoint) == target {
			selected = line
		}
	}
	if err := scanner.Err(); err != nil {
		return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: read mountinfo: %w", err)
	}
	if selected == "" {
		return SourceOverlay{}, fmt.Errorf("source_mount_not_found: exact mountpoint %q is absent", target)
	}
	return parseMountInfoOverlay(selected)
}

func parseMountInfoOverlay(line string) (SourceOverlay, error) {
	separator := strings.Index(line, " - ")
	if separator < 0 {
		return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: missing mountinfo separator")
	}
	left := strings.Fields(line[:separator])
	right := strings.Fields(line[separator+3:])
	if len(left) < 6 || len(right) < 3 {
		return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: incomplete mountinfo entry")
	}
	mountPoint, err := decodeMountInfoField(left[4])
	if err != nil {
		return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: decode mountpoint: %w", err)
	}
	if right[0] != "overlay" {
		return SourceOverlay{}, fmt.Errorf("source_filesystem_not_overlay: got %q", right[0])
	}
	values := map[string]string{}
	for _, option := range strings.Split(right[2], ",") {
		key, value, ok := strings.Cut(option, "=")
		if !ok || (key != "lowerdir" && key != "upperdir" && key != "workdir") {
			continue
		}
		if _, exists := values[key]; exists {
			return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: duplicate %s", key)
		}
		values[key] = value
	}
	for _, key := range []string{"lowerdir", "upperdir", "workdir"} {
		if values[key] == "" {
			return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: missing %s", key)
		}
	}
	upper, err := decodeMountInfoField(values["upperdir"])
	if err != nil || !filepath.IsAbs(upper) {
		return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: invalid upperdir %q", values["upperdir"])
	}
	work, err := decodeMountInfoField(values["workdir"])
	if err != nil || !filepath.IsAbs(work) {
		return SourceOverlay{}, fmt.Errorf("source_overlay_options_invalid: invalid workdir %q", values["workdir"])
	}
	lower, err := parseOverlayLowerdirs(values["lowerdir"], upper)
	if err != nil {
		return SourceOverlay{}, err
	}
	return SourceOverlay{
		MountPoint: mountPoint,
		LowerDirs:  lower,
		UpperDir:   filepath.Clean(upper),
		WorkDir:    filepath.Clean(work),
	}, nil
}

func parseOverlayLowerdirs(value, upper string) ([]string, error) {
	var lower []string
	for _, encoded := range strings.Split(value, ":") {
		path, err := decodeMountInfoField(encoded)
		if err != nil || path == "" {
			return nil, fmt.Errorf("source_overlay_options_invalid: invalid lowerdir %q", encoded)
		}
		if !filepath.IsAbs(path) && strings.HasPrefix(path, "l/") && filepath.Base(upper) == "diff" && filepath.Clean(path) == path && strings.Count(path, "/") == 1 {
			path = filepath.Join(filepath.Dir(filepath.Dir(upper)), path)
		}
		if !filepath.IsAbs(path) {
			// containerd compacts sibling snapshot paths to <id>/fs. Resolve
			// only that form against the absolute active upper's snapshot root;
			// the caller still compares every path with containerd's API and
			// validates allowed roots, symlinks and the empty active upper.
			id, leaf, ok := strings.Cut(path, "/")
			n, parseErr := strconv.ParseUint(id, 10, 64)
			if !ok || leaf != "fs" || parseErr != nil || n == 0 || filepath.Base(upper) != "fs" {
				return nil, fmt.Errorf("source_overlay_options_invalid: invalid relative lowerdir %q", path)
			}
			path = filepath.Join(filepath.Dir(filepath.Dir(upper)), path)
		}
		lower = append(lower, filepath.Clean(path))
	}
	return lower, nil
}

func decodeMountInfoField(value string) (string, error) {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+3 >= len(value) || !isOctal(value[i+1]) || !isOctal(value[i+2]) || !isOctal(value[i+3]) {
			return "", fmt.Errorf("invalid mountinfo escape at byte %d", i)
		}
		decoded := int(value[i+1]-'0')*64 + int(value[i+2]-'0')*8 + int(value[i+3]-'0')
		if decoded > 255 {
			return "", fmt.Errorf("mountinfo escape at byte %d is outside byte range", i)
		}
		out.WriteByte(byte(decoded))
		i += 4
	}
	return out.String(), nil
}

func isOctal(value byte) bool {
	return value >= '0' && value <= '7'
}

func validateSnapshotDir(path string, allowedRoots []string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("source_overlay_options_invalid: path %q is not absolute", path)
	}
	allowed := false
	var allowedRoot string
	var relativePath string
	for _, root := range allowedRoots {
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) {
			continue
		}
		rel, err := filepath.Rel(root, clean)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			allowed = true
			allowedRoot = root
			relativePath = rel
			break
		}
	}
	if !allowed {
		return fmt.Errorf("source_path_outside_allowlist: %q", clean)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("source_overlay_options_invalid: lstat %q: %w", clean, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("source_overlay_options_invalid: %q is not a real directory", clean)
	}
	evaluatedRoot, err := filepath.EvalSymlinks(allowedRoot)
	if err != nil {
		return fmt.Errorf("source_overlay_options_invalid: evaluate allowlist root %q: %w", allowedRoot, err)
	}
	evaluated, err := filepath.EvalSymlinks(clean)
	if err != nil || filepath.Clean(evaluated) != filepath.Clean(filepath.Join(evaluatedRoot, relativePath)) {
		return fmt.Errorf("source_overlay_options_invalid: %q contains a symlink", clean)
	}
	return nil
}

func equalPaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if filepath.Clean(a[i]) != filepath.Clean(b[i]) {
			return false
		}
	}
	return true
}
