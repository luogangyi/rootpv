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

// Package testutil 提供跨内部包复用的测试夹具。
package testutil

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

// PersistentRootfsPod 返回一个已声明 RootPV PVC 根文件系统的最小 Pod。
func PersistentRootfsPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-dev", Namespace: "ai-dev", Annotations: map[string]string{
			rootfs.AnnotationEnabled: "true", rootfs.AnnotationContainer: "dev", rootfs.AnnotationVolume: "rootfs-pvc", rootfs.AnnotationPath: "containers/dev",
		}},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "rootfs-pvc", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "gpu-dev-rootfs"},
		}}}, Containers: []corev1.Container{{Name: "dev", Image: "busybox"}}},
	}
}
