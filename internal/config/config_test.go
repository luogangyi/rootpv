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

package config

import (
	"strings"
	"testing"
)

func TestRuntimeMappingRejectsConflictingRegexMatches(t *testing.T) {
	mapping := RuntimeMapping{Regex: map[string]string{
		"^nvidia-.*$": "persistent-rootfs-nvidia",
		".*":          "persistent-rootfs-runc",
	}}

	_, err := mapping.Resolve("nvidia-a100", nil)
	if err == nil || !strings.Contains(err.Error(), "conflicting rootfs runtimeClass regexes") {
		t.Fatalf("Resolve error = %v, want conflicting regex error", err)
	}
}

func TestRuntimeMappingAllowsEquivalentRegexMatches(t *testing.T) {
	mapping := RuntimeMapping{Regex: map[string]string{
		"^nvidia-.*$": "persistent-rootfs-nvidia",
		".*":          "persistent-rootfs-nvidia",
	}}

	got, err := mapping.Resolve("nvidia-a100", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "persistent-rootfs-nvidia" {
		t.Fatalf("Resolve = %q, want persistent-rootfs-nvidia", got)
	}
}
