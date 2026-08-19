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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

type SpecStore interface {
	Load(bundle string) (*specs.Spec, error)
	Save(bundle string, spec *specs.Spec) error
}

type FileSpecStore struct{}

func (FileSpecStore) Load(bundle string) (*specs.Spec, error) {
	return LoadSpec(bundle)
}

func (FileSpecStore) Save(bundle string, spec *specs.Spec) error {
	return SaveSpec(bundle, spec)
}

func LoadSpec(bundle string) (*specs.Spec, error) {
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		return nil, err
	}
	var spec specs.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func SaveSpec(bundle string, spec *specs.Spec) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(bundle, "config.json")
	tmp := path + ".rootpv.tmp"
	return writeFileAtomic(tmp, path, data, 0o644)
}

func IsSandbox(spec *specs.Spec) bool {
	annotations := specAnnotations(spec)
	if labelValue(annotations, "io.kubernetes.cri.container-name", "io.kubernetes.container.name") == "POD" {
		return true
	}
	return labelValue(annotations, "io.kubernetes.container.type", "io.kubernetes.cri.container-type") == "sandbox" ||
		labelValue(annotations, "io.cri-containerd.kind") == "sandbox" ||
		strings.EqualFold(labelValue(annotations, "container-type"), "sandbox")
}

func ShouldMountPersistentRootfs(spec *specs.Spec) bool {
	if IsSandbox(spec) {
		return false
	}
	annotations := specAnnotations(spec)
	if !rootfs.PersistentRootfsEnabled(annotations) &&
		!strings.EqualFold(labelValue(annotations, rootfs.SnapshotLabelEnabled), "true") {
		return false
	}
	targets, configured := rootfsTargetContainers(annotations)
	if !configured {
		return true
	}
	containerName := labelValue(annotations, "io.kubernetes.cri.container-name", "io.kubernetes.container.name")
	return containerName != "" && targets[containerName]
}

func rootfsTargetContainers(annotations map[string]string) (map[string]bool, bool) {
	simpleTarget := strings.TrimSpace(annotations[rootfs.AnnotationContainer])
	advanced := strings.TrimSpace(annotations[rootfs.AnnotationSpec])
	if simpleTarget != "" {
		return map[string]bool{simpleTarget: true}, true
	}
	if advanced == "" {
		return nil, false
	}
	var specs []struct {
		ContainerName string `json:"containerName"`
	}
	if err := json.Unmarshal([]byte(advanced), &specs); err != nil {
		return map[string]bool{}, true
	}
	targets := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if name := strings.TrimSpace(spec.ContainerName); name != "" {
			targets[name] = true
		}
	}
	return targets, true
}

func BuildResolveRequest(spec *specs.Spec, inv Invocation) (apiv1alpha1.ResolveRootfsRequest, error) {
	annotations := cloneMap(specAnnotations(spec))
	originalRoot, err := OriginalRootPath(spec, inv.Bundle)
	if err != nil {
		return apiv1alpha1.ResolveRootfsRequest{}, err
	}

	req := apiv1alpha1.ResolveRootfsRequest{
		ContainerID:      inv.ContainerID,
		Bundle:           inv.Bundle,
		OriginalRootPath: originalRoot,
		RuntimeClass:     labelValue(annotations, rootfs.AnnotationEffectiveRuntimeClass, "io.kubernetes.cri.sandbox-runtime-handler"),
		Namespace:        labelValue(annotations, "io.kubernetes.pod.namespace", "io.kubernetes.cri.sandbox-namespace"),
		PodName:          labelValue(annotations, "io.kubernetes.pod.name", "io.kubernetes.cri.sandbox-name"),
		PodUID:           labelValue(annotations, "io.kubernetes.pod.uid", "io.kubernetes.cri.sandbox-uid"),
		ContainerName:    labelValue(annotations, "io.kubernetes.container.name", "io.kubernetes.cri.container-name"),
		SandboxID:        labelValue(annotations, "io.kubernetes.sandbox.id", "io.kubernetes.cri.sandbox-id"),
		Image:            labelValue(annotations, "io.kubernetes.cri.image-name"),
		ImageID:          labelValue(annotations, "containerd.io/snapshot/cri.image-ref", "cri.image-ref"),
		Labels:           annotations,
		Annotations:      annotations,
	}
	if spec != nil && spec.Linux != nil {
		req.MountLabel = spec.Linux.MountLabel
	}
	return req, nil
}

func OriginalRootPath(spec *specs.Spec, bundle string) (string, error) {
	if spec == nil || spec.Root == nil || spec.Root.Path == "" {
		return "", fmt.Errorf("OCI spec root.path is required")
	}
	if filepath.IsAbs(spec.Root.Path) {
		return spec.Root.Path, nil
	}
	if bundle == "" {
		return "", fmt.Errorf("bundle is required for relative root.path %q", spec.Root.Path)
	}
	return filepath.Join(bundle, spec.Root.Path), nil
}

func RewriteRoot(spec *specs.Spec, merged string) error {
	if spec.Root == nil {
		spec.Root = &specs.Root{}
	}
	spec.Root.Path = merged
	spec.Root.Readonly = false
	return nil
}

func specAnnotations(spec *specs.Spec) map[string]string {
	if spec == nil || spec.Annotations == nil {
		return map[string]string{}
	}
	return spec.Annotations
}

func labelValue(labels map[string]string, names ...string) string {
	return rootfs.LabelValue(labels, names...)
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
