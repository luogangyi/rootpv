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

package rootfs

import (
	"testing"
)

func TestParsePodSpecDisabled(t *testing.T) {
	specs, enabled, err := ParsePodSpec(map[string]string{}, []string{"dev"}, []VolumeRef{{Name: "rootfs-pvc", PVCName: "pvc"}})
	if err != nil {
		t.Fatalf("ParsePodSpec returned error: %v", err)
	}
	if enabled {
		t.Fatalf("expected disabled rootfs")
	}
	if len(specs) != 0 {
		t.Fatalf("expected no specs, got %d", len(specs))
	}
}

func TestParsePodSpecSimpleAnnotations(t *testing.T) {
	annotations := map[string]string{
		AnnotationEnabled:         "true",
		AnnotationContainer:       "dev",
		AnnotationVolume:          "rootfs-pvc",
		AnnotationPath:            "containers/dev",
		AnnotationBaseImagePolicy: "Reset",
		AnnotationQuota:           "50Gi",
	}

	specs, enabled, err := ParsePodSpec(annotations, []string{"dev"}, []VolumeRef{{Name: "rootfs-pvc", PVCName: "gpu-dev-rootfs"}})
	if err != nil {
		t.Fatalf("ParsePodSpec returned error: %v", err)
	}
	if !enabled {
		t.Fatalf("expected enabled rootfs")
	}
	if len(specs) != 1 {
		t.Fatalf("expected one spec, got %d", len(specs))
	}
	got := specs[0]
	if got.ContainerName != "dev" || got.VolumeName != "rootfs-pvc" || got.PVCName != "gpu-dev-rootfs" {
		t.Fatalf("unexpected target: %+v", got)
	}
	if got.Path != "containers/dev" || got.BaseImagePolicy != BaseImagePolicyReset || got.Quota != "50Gi" {
		t.Fatalf("unexpected normalized spec: %+v", got)
	}
}

func TestParsePodSpecWrapperMarker(t *testing.T) {
	annotations := map[string]string{
		AnnotationEnabled:        "wrapper",
		AnnotationWrapperEnabled: "true",
		AnnotationContainer:      "dev",
		AnnotationVolume:         "rootfs-pvc",
		AnnotationPath:           "containers/dev",
	}

	specs, enabled, err := ParsePodSpec(annotations, []string{"dev"}, []VolumeRef{{Name: "rootfs-pvc", PVCName: "pvc"}})
	if err != nil {
		t.Fatalf("ParsePodSpec returned error: %v", err)
	}
	if !enabled || len(specs) != 1 {
		t.Fatalf("expected one enabled wrapper spec, got enabled=%t specs=%d", enabled, len(specs))
	}
}

func TestParsePodSpecJSONAnnotations(t *testing.T) {
	annotations := map[string]string{
		AnnotationEnabled: "true",
		AnnotationSpec: `[
			{"containerName":"notebook","volumeName":"rootfs-pvc","path":"containers/notebook"},
			{"containerName":"worker","volumeName":"rootfs-pvc","path":"containers/worker","baseImagePolicy":"Rebase"}
		]`,
	}

	specs, enabled, err := ParsePodSpec(annotations, []string{"notebook", "worker"}, []VolumeRef{{Name: "rootfs-pvc", PVCName: "shared-rootfs"}})
	if err != nil {
		t.Fatalf("ParsePodSpec returned error: %v", err)
	}
	if !enabled {
		t.Fatalf("expected enabled rootfs")
	}
	if len(specs) != 2 {
		t.Fatalf("expected two specs, got %d", len(specs))
	}
	wantPolicies := map[string]struct{}{BaseImagePolicyStrict: {}, BaseImagePolicyRebase: {}}
	for _, spec := range specs {
		if _, ok := wantPolicies[spec.BaseImagePolicy]; !ok {
			t.Fatalf("unexpected rootfs policy: %q", spec.BaseImagePolicy)
		}
		delete(wantPolicies, spec.BaseImagePolicy)
	}
	if len(wantPolicies) != 0 {
		t.Fatalf("missing expected rootfs policies: %#v", wantPolicies)
	}
}

func TestParsePodSpecRejectsInvalidConfig(t *testing.T) {
	assertInvalidPodSpecs(t, invalidSimplePodSpecCases())
	assertInvalidPodSpecs(t, invalidPolicyPodSpecCases())
}

type invalidPodSpecCase struct {
	name       string
	annotation map[string]string
}

func invalidSimplePodSpecCases() []invalidPodSpecCase {
	return []invalidPodSpecCase{
		{
			name: "missing container",
			annotation: map[string]string{
				AnnotationEnabled:   "true",
				AnnotationContainer: "missing",
				AnnotationVolume:    "rootfs-pvc",
				AnnotationPath:      "containers/dev",
			},
		},
		{
			name: "missing volume",
			annotation: map[string]string{
				AnnotationEnabled:   "true",
				AnnotationContainer: "dev",
				AnnotationVolume:    "missing",
				AnnotationPath:      "containers/dev",
			},
		},
		{
			name: "absolute path",
			annotation: map[string]string{
				AnnotationEnabled:   "true",
				AnnotationContainer: "dev",
				AnnotationVolume:    "rootfs-pvc",
				AnnotationPath:      "/containers/dev",
			},
		},
		{
			name: "parent path",
			annotation: map[string]string{
				AnnotationEnabled:   "true",
				AnnotationContainer: "dev",
				AnnotationVolume:    "rootfs-pvc",
				AnnotationPath:      "../dev",
			},
		},
		{
			name: "anchor conflict",
			annotation: map[string]string{
				AnnotationEnabled:   "true",
				AnnotationContainer: "dev",
				AnnotationVolume:    "rootfs-pvc",
				AnnotationPath:      ".rootpv-anchor/dev",
			},
		},
	}
}

func invalidPolicyPodSpecCases() []invalidPodSpecCase {
	return []invalidPodSpecCase{
		{name: "invalid base image policy", annotation: map[string]string{AnnotationEnabled: "true", AnnotationContainer: "dev", AnnotationVolume: "rootfs-pvc", AnnotationPath: "containers/dev", AnnotationBaseImagePolicy: "Merge"}},
		{
			name: "duplicate pvc path",
			annotation: map[string]string{
				AnnotationEnabled: "true",
				AnnotationSpec: `[
					{"containerName":"dev","volumeName":"rootfs-pvc","path":"containers/shared"},
					{"containerName":"worker","volumeName":"rootfs-pvc","path":"containers/shared"}
				]`,
			},
		},
	}
}

func assertInvalidPodSpecs(t *testing.T, tests []invalidPodSpecCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ParsePodSpec(tt.annotation, []string{"dev", "worker"}, []VolumeRef{{Name: "rootfs-pvc", PVCName: "pvc"}})
			if err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestParsePodSpecRejectsConflictingFormats(t *testing.T) {
	annotations := map[string]string{
		AnnotationEnabled:   "true",
		AnnotationContainer: "dev",
		AnnotationVolume:    "rootfs-pvc",
		AnnotationPath:      "containers/dev",
		AnnotationSpec:      `[{"containerName":"dev","volumeName":"rootfs-pvc","path":"containers/dev"}]`,
	}

	_, _, err := ParsePodSpec(annotations, []string{"dev"}, []VolumeRef{{Name: "rootfs-pvc", PVCName: "pvc"}})
	if err == nil {
		t.Fatalf("expected conflicting format error")
	}
}
