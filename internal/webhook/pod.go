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

package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

const (
	MutationVersion = "v1.2"
	AnchorMountRoot = "/var/run/rootpv/rootfs-anchor"
	PodNameEnvName  = "ROOTPV_POD_NAME"
)

type runtimeRoute struct {
	original  string
	effective string
	adapter   config.RuntimeAdapter
}

// MutatePod 仅改写显式启用 RootPV 的容器：选择对应 runtime adapter，写入不可变路由证据，
// 并注入只读 anchor 挂载，让节点侧可将 PVC 路径与目标容器关联起来。
func MutatePod(pod *corev1.Pod, cfg config.Config) (bool, error) {
	if cfg.MutationVersion == "" {
		cfg.MutationVersion = MutationVersion
	}
	specs, enabled, err := rootfs.ParsePodSpec(pod.Annotations, rootfs.PodContainerNames(pod), rootfs.PodPVCVolumes(pod))
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	route, err := mutateRuntimeRouting(pod, cfg)
	if err != nil {
		return false, err
	}
	if err := writeMutationAnnotations(pod, cfg, route, specs); err != nil {
		return false, err
	}
	injectRootfsAnchors(pod, specs)
	return true, nil
}

// mutateRuntimeRouting 计算并写入 Pod 实际使用的 RuntimeClass，重复 mutation 时恢复最初的用户选择。
func mutateRuntimeRouting(pod *corev1.Pod, cfg config.Config) (runtimeRoute, error) {
	originalRuntime := ""
	if pod.Spec.RuntimeClassName != nil {
		originalRuntime = *pod.Spec.RuntimeClassName
	}
	if pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] == originalRuntime {
		originalRuntime = pod.Annotations[rootfs.AnnotationOriginalRuntimeClass]
	}
	effectiveRuntime, adapter, err := effectiveRuntimeAdapter(cfg, originalRuntime, resourceNames(pod))
	if err != nil {
		return runtimeRoute{}, err
	}
	if cfg.RuntimeClassMode != config.RuntimeClassModePreserve || effectiveRuntime != "" {
		pod.Spec.RuntimeClassName = &effectiveRuntime
	}
	return runtimeRoute{original: originalRuntime, effective: effectiveRuntime, adapter: adapter}, nil
}

// writeMutationAnnotations 固化 admission 选择的路由证据和持久根文件系统配置摘要。
func writeMutationAnnotations(pod *corev1.Pod, cfg config.Config, route runtimeRoute, specs []rootfs.ContainerRootfsSpec) error {
	pod.Annotations[rootfs.AnnotationOriginalRuntimeClass] = route.original
	pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] = route.effective
	if cfg.RuntimeAdapterCutover {
		digest, err := route.adapter.ClusterDigest()
		if err != nil {
			return err
		}
		pod.Annotations[rootfs.AnnotationRuntimeAdapterID] = route.adapter.ID
		pod.Annotations[rootfs.AnnotationRuntimeAdapterMode] = string(route.adapter.Mode)
		pod.Annotations[rootfs.AnnotationRuntimeAdapterConfigHash] = digest
	}
	pod.Annotations[rootfs.AnnotationMutated] = cfg.MutationVersion
	if cfg.RuntimeClassMode != config.RuntimeClassModePreserve {
		pod.Annotations[rootfs.AnnotationEnabled] = "wrapper"
		pod.Annotations[rootfs.AnnotationWrapperEnabled] = "true"
	}
	digest, err := specHash(specs)
	if err != nil {
		return err
	}
	pod.Annotations[rootfs.AnnotationConfigHash] = digest
	return nil
}

// injectRootfsAnchors 只为显式启用持久 rootfs 的业务容器注入只读 PVC anchor。
func injectRootfsAnchors(pod *corev1.Pod, specs []rootfs.ContainerRootfsSpec) {
	specByContainer := make(map[string]rootfs.ContainerRootfsSpec, len(specs))
	for _, spec := range specs {
		specByContainer[spec.ContainerName] = spec
	}
	for index := range pod.Spec.Containers {
		spec, ok := specByContainer[pod.Spec.Containers[index].Name]
		if !ok {
			continue
		}
		ensurePodNameEnv(&pod.Spec.Containers[index])
		ensureAnchorMount(&pod.Spec.Containers[index], spec)
	}
}

// ValidatePod 复算关键路由字段并拒绝不一致对象，防止用户绕过 MutatingWebhook 篡改 adapter 证据。
func ValidatePod(pod *corev1.Pod, cfg config.Config) error {
	specs, enabled, err := rootfs.ParsePodSpec(pod.Annotations, rootfs.PodContainerNames(pod), rootfs.PodPVCVolumes(pod))
	if err != nil || !enabled {
		return err
	}
	originalRuntime := ""
	if pod.Spec.RuntimeClassName != nil {
		originalRuntime = *pod.Spec.RuntimeClassName
	}
	if pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] == originalRuntime {
		originalRuntime = pod.Annotations[rootfs.AnnotationOriginalRuntimeClass]
	}
	effectiveRuntime, adapter, err := effectiveRuntimeAdapter(cfg, originalRuntime, resourceNames(pod))
	if err != nil {
		return err
	}
	if cfg.RuntimeAdapterCutover {
		digest, err := adapter.ClusterDigest()
		if err != nil {
			return err
		}
		if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != effectiveRuntime ||
			pod.Annotations[rootfs.AnnotationOriginalRuntimeClass] != originalRuntime ||
			pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] != effectiveRuntime ||
			pod.Annotations[rootfs.AnnotationRuntimeAdapterID] != adapter.ID ||
			pod.Annotations[rootfs.AnnotationRuntimeAdapterMode] != string(adapter.Mode) ||
			pod.Annotations[rootfs.AnnotationRuntimeAdapterConfigHash] != digest {
			return fmt.Errorf("runtime_adapter_state_mismatch")
		}
	}
	if len(specs) == 0 {
		return fmt.Errorf("persistent rootfs is enabled but no rootfs target exists")
	}
	return nil
}

func effectiveRuntimeClass(cfg config.Config, originalRuntime string, resources []string) (string, error) {
	if cfg.RuntimeClassMode == config.RuntimeClassModePreserve {
		return originalRuntime, nil
	}
	return cfg.RuntimeMapping.Resolve(originalRuntime, resources)
}

// effectiveRuntimeAdapter 在切换期优先使用运行时目录；旧模式则沿用 RuntimeClass 映射规则。
func effectiveRuntimeAdapter(cfg config.Config, originalRuntime string, resources []string) (string, config.RuntimeAdapter, error) {
	if cfg.RuntimeAdapterCutover {
		if cfg.RuntimeCatalog.FormatVersion == config.RuntimeCatalogFormatVersion {
			adapter, err := cfg.RuntimeCatalog.ResolveAdmissionAdapter(originalRuntime)
			if err != nil {
				return "", config.RuntimeAdapter{}, err
			}
			return adapter.RootfsRuntimeClass, adapter, nil
		}
		adapter, err := cfg.RuntimeAdapters.Resolve(originalRuntime)
		if err != nil {
			return "", config.RuntimeAdapter{}, err
		}
		return adapter.RootfsRuntimeClass, adapter, nil
	}
	effective, err := effectiveRuntimeClass(cfg, originalRuntime, resources)
	return effective, config.RuntimeAdapter{}, err
}

func ensurePodNameEnv(container *corev1.Container) {
	valueFrom := &corev1.EnvVarSource{
		FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
	}
	for i, env := range container.Env {
		if env.Name == PodNameEnvName {
			container.Env[i].Value = ""
			container.Env[i].ValueFrom = valueFrom
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{
		Name:      PodNameEnvName,
		ValueFrom: valueFrom,
	})
}

// ensureAnchorMount 通过 SubPathExpr 使用 Pod 名隔离同一 PVC 中不同 Pod/容器的 RootPV 目录。
func ensureAnchorMount(container *corev1.Container, spec rootfs.ContainerRootfsSpec) {
	mountPath := AnchorMountRoot + "/" + spec.VolumeName
	subPathExpr := ".rootpv-anchor/$(" + PodNameEnvName + ")/" + spec.ContainerName
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == spec.VolumeName && container.VolumeMounts[i].MountPath == mountPath {
			container.VolumeMounts[i].ReadOnly = true
			container.VolumeMounts[i].SubPathExpr = subPathExpr
			return
		}
	}
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:        spec.VolumeName,
		MountPath:   mountPath,
		ReadOnly:    true,
		SubPathExpr: subPathExpr,
	})
}

func resourceNames(pod *corev1.Pod) []string {
	seen := map[string]struct{}{}
	for _, container := range pod.Spec.Containers {
		for name := range container.Resources.Limits {
			seen[string(name)] = struct{}{}
		}
		for name := range container.Resources.Requests {
			seen[string(name)] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func specHash(specs []rootfs.ContainerRootfsSpec) (string, error) {
	data, err := json.Marshal(specs)
	if err != nil {
		return "", fmt.Errorf("marshal rootfs spec for hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
