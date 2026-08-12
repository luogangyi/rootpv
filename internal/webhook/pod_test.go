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
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/apimachinery/pkg/api/resource"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/testutil"
)

func TestMutatePodInjectsRuntimeAnchorAndAuditAnnotations(t *testing.T) {
	pod := gpuAnnotatedPod()

	mutated, err := MutatePod(pod, testConfig())
	if err != nil {
		t.Fatalf("MutatePod returned error: %v", err)
	}
	if !mutated {
		t.Fatalf("expected mutation")
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "persistent-rootfs-nvidia" {
		t.Fatalf("unexpected runtimeClassName: %v", pod.Spec.RuntimeClassName)
	}
	if pod.Annotations[rootfs.AnnotationOriginalRuntimeClass] != "" {
		t.Fatalf("expected empty original runtime class annotation, got %q", pod.Annotations[rootfs.AnnotationOriginalRuntimeClass])
	}
	if pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] != "persistent-rootfs-nvidia" {
		t.Fatalf("expected effective runtime annotation")
	}
	if pod.Annotations[rootfs.AnnotationMutated] != MutationVersion {
		t.Fatalf("expected mutation version annotation")
	}
	if pod.Annotations[rootfs.AnnotationEnabled] != "wrapper" || pod.Annotations[rootfs.AnnotationWrapperEnabled] != "true" {
		t.Fatalf("expected wrapper enablement markers, got %#v", pod.Annotations)
	}
	if _, ok := pod.Annotations[rootfs.SnapshotLabelEnabled]; ok {
		t.Fatalf("snapshot label %q must not be injected as a Kubernetes annotation", rootfs.SnapshotLabelEnabled)
	}

	container := pod.Spec.Containers[0]
	if !hasEnv(container, "ROOTPV_POD_NAME") {
		t.Fatalf("expected ROOTPV_POD_NAME env")
	}
	mount := findMount(container, "rootfs-pvc")
	if mount == nil {
		t.Fatalf("expected anchor volumeMount")
	}
	if mount.MountPath != "/var/run/rootpv/rootfs-anchor/rootfs-pvc" {
		t.Fatalf("unexpected mount path: %q", mount.MountPath)
	}
	if !mount.ReadOnly {
		t.Fatalf("expected readonly anchor mount")
	}
	if mount.SubPathExpr != ".rootpv-anchor/$(ROOTPV_POD_NAME)/dev" {
		t.Fatalf("unexpected subPathExpr: %q", mount.SubPathExpr)
	}
}

func TestMutatePodUsesExactRuntimeMapping(t *testing.T) {
	runtime := "nvidia"
	pod := annotatedPod()
	pod.Spec.RuntimeClassName = &runtime

	_, err := MutatePod(pod, testConfig())
	if err != nil {
		t.Fatalf("MutatePod returned error: %v", err)
	}
	if got := *pod.Spec.RuntimeClassName; got != "persistent-rootfs-nvidia" {
		t.Fatalf("expected exact mapping, got %q", got)
	}
	if pod.Annotations[rootfs.AnnotationOriginalRuntimeClass] != "nvidia" {
		t.Fatalf("expected original runtime annotation")
	}
}

func TestMutatePodPreservesRuntimeClassInSnapshotterMode(t *testing.T) {
	runtime := "nvidia"
	pod := annotatedPod()
	pod.Spec.RuntimeClassName = &runtime
	cfg := testConfig()
	cfg.RuntimeClassMode = config.RuntimeClassModePreserve

	mutated, err := MutatePod(pod, cfg)
	if err != nil {
		t.Fatalf("MutatePod returned error: %v", err)
	}
	if !mutated {
		t.Fatalf("expected mutation")
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "nvidia" {
		t.Fatalf("expected runtimeClassName to be preserved, got %v", pod.Spec.RuntimeClassName)
	}
	if pod.Annotations[rootfs.AnnotationOriginalRuntimeClass] != "nvidia" {
		t.Fatalf("expected original runtime annotation to record preserved runtime, got %q", pod.Annotations[rootfs.AnnotationOriginalRuntimeClass])
	}
	if pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] != "nvidia" {
		t.Fatalf("expected effective runtime annotation to record preserved runtime, got %q", pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass])
	}
	if pod.Annotations[rootfs.AnnotationEnabled] != "true" || pod.Annotations[rootfs.AnnotationWrapperEnabled] != "" {
		t.Fatalf("snapshotter mode must retain the public enablement annotation: %#v", pod.Annotations)
	}
}

func TestMutatePodPreserveModeAllowsEmptyRuntimeClass(t *testing.T) {
	pod := annotatedPod()
	cfg := testConfig()
	cfg.RuntimeClassMode = config.RuntimeClassModePreserve

	mutated, err := MutatePod(pod, cfg)
	if err != nil {
		t.Fatalf("MutatePod returned error: %v", err)
	}
	if !mutated {
		t.Fatalf("expected mutation")
	}
	if pod.Spec.RuntimeClassName != nil {
		t.Fatalf("expected runtimeClassName to remain empty, got %q", *pod.Spec.RuntimeClassName)
	}
	if pod.Annotations[rootfs.AnnotationOriginalRuntimeClass] != "" {
		t.Fatalf("expected empty original runtime annotation, got %q", pod.Annotations[rootfs.AnnotationOriginalRuntimeClass])
	}
	if pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass] != "" {
		t.Fatalf("expected empty effective runtime annotation, got %q", pod.Annotations[rootfs.AnnotationEffectiveRuntimeClass])
	}
}

func TestMutatePodLeavesUnannotatedPodUnchanged(t *testing.T) {
	pod := annotatedPod()
	pod.Annotations = map[string]string{}

	mutated, err := MutatePod(pod, testConfig())
	if err != nil {
		t.Fatalf("MutatePod returned error: %v", err)
	}
	if mutated {
		t.Fatalf("expected no mutation")
	}
	if pod.Spec.RuntimeClassName != nil {
		t.Fatalf("expected no runtime class")
	}
}

func TestMutatePodRejectsInvalidRootfsConfig(t *testing.T) {
	pod := annotatedPod()
	pod.Annotations[rootfs.AnnotationVolume] = "missing"

	_, err := MutatePod(pod, testConfig())
	if err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestMutatePodIsIdempotent(t *testing.T) {
	pod := annotatedPod()
	if _, err := MutatePod(pod, testConfig()); err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if _, err := MutatePod(pod, testConfig()); err != nil {
		t.Fatalf("second mutation: %v", err)
	}

	container := pod.Spec.Containers[0]
	envCount := 0
	mountCount := 0
	for _, env := range container.Env {
		if env.Name == "ROOTPV_POD_NAME" {
			envCount++
		}
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == "rootfs-pvc" && mount.MountPath == "/var/run/rootpv/rootfs-anchor/rootfs-pvc" {
			mountCount++
		}
	}
	if envCount != 1 || mountCount != 1 {
		t.Fatalf("expected idempotent env/mount injection, env=%d mount=%d", envCount, mountCount)
	}
}

func TestMutatePodOverridesSpoofedPodNameEnv(t *testing.T) {
	pod := annotatedPod()
	pod.Spec.Containers[0].Env = []corev1.EnvVar{{
		Name:  PodNameEnvName,
		Value: "spoofed-name",
	}}

	if _, err := MutatePod(pod, testConfig()); err != nil {
		t.Fatalf("MutatePod returned error: %v", err)
	}

	container := pod.Spec.Containers[0]
	envCount := 0
	for _, env := range container.Env {
		if env.Name != PodNameEnvName {
			continue
		}
		envCount++
		if env.Value != "" {
			t.Fatalf("expected pod name env value to be cleared, got %q", env.Value)
		}
		if env.ValueFrom == nil || env.ValueFrom.FieldRef == nil || env.ValueFrom.FieldRef.FieldPath != "metadata.name" {
			t.Fatalf("expected pod name env to use metadata.name fieldRef, got %+v", env)
		}
	}
	if envCount != 1 {
		t.Fatalf("expected one pod name env, got %d", envCount)
	}
}

func TestValidatePodAcceptsAlreadyMutatedPod(t *testing.T) {
	pod := annotatedPod()
	if _, err := MutatePod(pod, testConfig()); err != nil {
		t.Fatalf("mutation: %v", err)
	}
	if err := ValidatePod(pod, testConfig()); err != nil {
		t.Fatalf("ValidatePod should accept already-mutated pod: %v", err)
	}
}

func annotatedPod() *corev1.Pod {
	return testutil.PersistentRootfsPod()
}

func gpuAnnotatedPod() *corev1.Pod {
	pod := annotatedPod()
	pod.Spec.Containers[0].Image = "nvidia/cuda:12.4.1-base-ubuntu22.04"
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{
		corev1.ResourceName("nvidia.com/gpu"): resourceapi.MustParse("1"),
	}
	return pod
}

func testConfig() config.Config {
	return config.Config{
		MutationVersion: MutationVersion,
		RuntimeMapping: config.RuntimeMapping{
			DefaultRootfsRuntimeClass: "persistent-rootfs-runc",
			Exact: map[string]string{
				"":       "persistent-rootfs-runc",
				"runc":   "persistent-rootfs-runc",
				"nvidia": "persistent-rootfs-nvidia",
			},
			ResourceHints: map[string]string{
				"nvidia.com/gpu": "persistent-rootfs-nvidia",
			},
		},
	}
}

func hasEnv(container corev1.Container, name string) bool {
	for _, env := range container.Env {
		if env.Name == name {
			return true
		}
	}
	return false
}

func findMount(container corev1.Container, volume string) *corev1.VolumeMount {
	for i := range container.VolumeMounts {
		if container.VolumeMounts[i].Name == volume {
			return &container.VolumeMounts[i]
		}
	}
	return nil
}
