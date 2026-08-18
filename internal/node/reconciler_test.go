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

package node

import (
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

func TestPodRootfsFromPodBuildsRootfsRegistration(t *testing.T) {
	tmp := t.TempDir()
	mountPath := filepath.Join(tmp, "pods", "pod-uid-1", "volumes", "kubernetes.io~local-volume", "rootfs-pvc")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gpu-dev",
			Namespace: "ai-dev",
			UID:       types.UID("pod-uid-1"),
			Annotations: map[string]string{
				rootfs.AnnotationEnabled:   "true",
				rootfs.AnnotationContainer: "dev",
				rootfs.AnnotationVolume:    "rootfs-pvc",
				rootfs.AnnotationPath:      "containers/dev",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "nv-test-gpu2",
			Volumes: []corev1.Volume{{
				Name: "rootfs-pvc",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "gpu-dev-rootfs"},
				},
			}},
			Containers: []corev1.Container{{Name: "dev"}},
		},
	}

	got, ok, err := PodRootfsFromPod(pod, tmp)
	if err != nil {
		t.Fatalf("PodRootfsFromPod: %v", err)
	}
	if !ok {
		t.Fatalf("expected enabled pod")
	}
	if got.PodUID != "pod-uid-1" || got.NodeName != "nv-test-gpu2" {
		t.Fatalf("unexpected pod identity: %+v", got)
	}
	if got.PVCHostPaths["rootfs-pvc"] != mountPath {
		t.Fatalf("unexpected pvc host path: %q", got.PVCHostPaths["rootfs-pvc"])
	}
	if len(got.Specs) != 1 || got.Specs[0].PVCName != "gpu-dev-rootfs" {
		t.Fatalf("unexpected rootfs specs: %+v", got.Specs)
	}
}

func TestPodRootfsFromPodIgnoresUnannotatedPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default", UID: types.UID("uid")},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	_, ok, err := PodRootfsFromPod(pod, t.TempDir())
	if err != nil {
		t.Fatalf("PodRootfsFromPod: %v", err)
	}
	if ok {
		t.Fatalf("expected unannotated pod to be ignored")
	}
}

func TestRootfsEnabledAcceptsWrapperMarker(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		rootfs.AnnotationEnabled:        "wrapper",
		rootfs.AnnotationWrapperEnabled: "true",
	}}}
	if !rootfsEnabled(pod) {
		t.Fatal("wrapper marker must keep the pod registered for rootfs reconciliation")
	}
}
