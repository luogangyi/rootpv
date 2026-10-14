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
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	SnapshotLabelPrefix = "containerd.io/snapshot/"

	AnnotationEnabled = "openfuyao.cn/persistent-rootfs"
	// AnnotationWrapperEnabled is the controller-owned marker used after a
	// wrapper-mode admission mutation.  Keeping the user-facing annotation from
	// being exactly "true" prevents legacy legacy containerd patch builds from
	// selecting the removed legacy proxy snapshotter.
	AnnotationWrapperEnabled  = "openfuyao.cn/persistent-rootfs-wrapper"
	AnnotationContainer       = "openfuyao.cn/rootfs-container"
	AnnotationVolume          = "openfuyao.cn/rootfs-volume"
	AnnotationPath            = "openfuyao.cn/rootfs-path"
	AnnotationBaseImagePolicy = "openfuyao.cn/rootfs-base-image-policy"
	AnnotationQuota           = "openfuyao.cn/rootfs-quota"
	AnnotationSpec            = "openfuyao.cn/rootfs.spec"

	SnapshotLabelEnabled = "containerd.io/snapshot/openfuyao.cn/persistent-rootfs"

	AnnotationOriginalRuntimeClass     = "openfuyao.cn/original-runtime-class"
	AnnotationEffectiveRuntimeClass    = "openfuyao.cn/effective-runtime-class"
	AnnotationRuntimeAdapterID         = "openfuyao.cn/runtime-adapter-id"
	AnnotationRuntimeAdapterMode       = "openfuyao.cn/runtime-adapter-mode"
	AnnotationRuntimeAdapterConfigHash = "openfuyao.cn/runtime-adapter-config-hash"
	AnnotationMutated                  = "openfuyao.cn/persistent-rootfs-mutated"
	AnnotationConfigHash               = "openfuyao.cn/persistent-rootfs-config-hash"

	BaseImagePolicyStrict = "Strict"
	BaseImagePolicyReset  = "Reset"
	BaseImagePolicyRebase = "Rebase"

	DefaultMode = "overlay-upper"
)

var validBaseImagePolicies = map[string]struct{}{
	BaseImagePolicyStrict: {},
	BaseImagePolicyReset:  {},
	BaseImagePolicyRebase: {},
}

type VolumeRef struct {
	Name    string
	PVCName string
}

// PodContainerNames 返回 Pod 中业务容器的名称，用于将注解配置与容器身份绑定。
func PodContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		names = append(names, container.Name)
	}
	return names
}

// PodPVCVolumes 返回 Pod 中 PVC 类型卷的名称和声明名，供配置校验及节点路径发现共用。
func PodPVCVolumes(pod *corev1.Pod) []VolumeRef {
	volumes := make([]VolumeRef, 0, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil {
			continue
		}
		volumes = append(volumes, VolumeRef{Name: volume.Name, PVCName: volume.PersistentVolumeClaim.ClaimName})
	}
	return volumes
}

type ContainerRootfsSpec struct {
	ContainerName   string `json:"containerName"`
	VolumeName      string `json:"volumeName"`
	PVCName         string `json:"-"`
	Path            string `json:"path"`
	Mode            string `json:"mode,omitempty"`
	BaseImagePolicy string `json:"baseImagePolicy,omitempty"`
	Quota           string `json:"quota,omitempty"`
}

func ParsePodSpec(annotations map[string]string, containers []string, volumes []VolumeRef) ([]ContainerRootfsSpec, bool, error) {
	if !PersistentRootfsEnabled(annotations) {
		return nil, false, nil
	}

	simple := hasAnyAnnotation(annotations, AnnotationContainer, AnnotationVolume, AnnotationPath, AnnotationBaseImagePolicy, AnnotationQuota)
	advanced := strings.TrimSpace(annotations[AnnotationSpec]) != ""
	if simple && advanced {
		return nil, true, errors.New("simple rootfs annotations and openfuyao.cn/rootfs.spec cannot be used together")
	}

	var specs []ContainerRootfsSpec
	if simple {
		specs = []ContainerRootfsSpec{{
			ContainerName:   annotations[AnnotationContainer],
			VolumeName:      annotations[AnnotationVolume],
			Path:            annotations[AnnotationPath],
			BaseImagePolicy: annotations[AnnotationBaseImagePolicy],
			Quota:           annotations[AnnotationQuota],
		}}
	} else if advanced {
		if err := json.Unmarshal([]byte(annotations[AnnotationSpec]), &specs); err != nil {
			return nil, true, fmt.Errorf("parse %s: %w", AnnotationSpec, err)
		}
	} else {
		return nil, true, errors.New("persistent rootfs is enabled but no rootfs config was provided")
	}

	if err := normalizeAndValidate(specs, containers, volumes); err != nil {
		return nil, true, err
	}
	sort.SliceStable(specs, func(i, j int) bool {
		return specs[i].ContainerName < specs[j].ContainerName
	})
	return specs, true, nil
}

// PersistentRootfsEnabled accepts either the public request annotation or the
// internal wrapper-mode marker.  The latter is written only by the admission
// controller after it has validated the public request.
func PersistentRootfsEnabled(annotations map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(annotations[AnnotationEnabled]), "true") ||
		strings.EqualFold(strings.TrimSpace(annotations[AnnotationWrapperEnabled]), "true")
}

// LabelValue 读取普通标签或 containerd 透传的 snapshot 标签中的第一个非空值。
func LabelValue(labels map[string]string, names ...string) string {
	for _, name := range names {
		if labels[name] != "" {
			return labels[name]
		}
		if !strings.HasPrefix(name, SnapshotLabelPrefix) && labels[SnapshotLabelPrefix+name] != "" {
			return labels[SnapshotLabelPrefix+name]
		}
	}
	return ""
}

func hasAnyAnnotation(annotations map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(annotations[key]) != "" {
			return true
		}
	}
	return false
}

func normalizeAndValidate(specs []ContainerRootfsSpec, containers []string, volumes []VolumeRef) error {
	if len(specs) == 0 {
		return errors.New("rootfs config must contain at least one container")
	}

	containerSet := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		containerSet[container] = struct{}{}
	}
	volumeByName := make(map[string]VolumeRef, len(volumes))
	for _, volume := range volumes {
		volumeByName[volume.Name] = volume
	}

	seenPath := map[string]string{}
	for i := range specs {
		spec := &specs[i]
		spec.ContainerName = strings.TrimSpace(spec.ContainerName)
		spec.VolumeName = strings.TrimSpace(spec.VolumeName)
		spec.Path = filepath.ToSlash(strings.TrimSpace(spec.Path))
		spec.Mode = strings.TrimSpace(spec.Mode)
		spec.BaseImagePolicy = strings.TrimSpace(spec.BaseImagePolicy)
		spec.Quota = strings.TrimSpace(spec.Quota)

		if spec.ContainerName == "" {
			return errors.New("rootfs containerName is required")
		}
		if _, ok := containerSet[spec.ContainerName]; !ok {
			return fmt.Errorf("rootfs container %q does not exist", spec.ContainerName)
		}
		volume, ok := volumeByName[spec.VolumeName]
		if !ok || volume.PVCName == "" {
			return fmt.Errorf("rootfs volume %q must be an existing persistentVolumeClaim volume", spec.VolumeName)
		}
		spec.PVCName = volume.PVCName

		if spec.Mode == "" {
			spec.Mode = DefaultMode
		}
		if spec.Mode != DefaultMode {
			return fmt.Errorf("unsupported rootfs mode %q", spec.Mode)
		}
		if spec.BaseImagePolicy == "" {
			spec.BaseImagePolicy = BaseImagePolicyStrict
		}
		if _, ok := validBaseImagePolicies[spec.BaseImagePolicy]; !ok {
			return fmt.Errorf("unsupported baseImagePolicy %q", spec.BaseImagePolicy)
		}
		if err := validateRootfsPath(spec.Path); err != nil {
			return fmt.Errorf("invalid rootfs path for container %q: %w", spec.ContainerName, err)
		}
		pathKey := spec.VolumeName + ":" + spec.Path
		if owner, ok := seenPath[pathKey]; ok {
			return fmt.Errorf("containers %q and %q share rootfs volume/path %s", owner, spec.ContainerName, pathKey)
		}
		seenPath[pathKey] = spec.ContainerName
	}
	return nil
}

func validateRootfsPath(path string) error {
	if path == "" || path == "." {
		return errors.New("path is required")
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return errors.New("path must be relative")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return errors.New("path must not contain parent traversal")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".rootpv-anchor" {
			return errors.New("path must not overlap .rootpv-anchor")
		}
	}
	return nil
}
