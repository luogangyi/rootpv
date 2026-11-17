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
	"context"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/rootfsd"
)

func StartPodReconciler(ctx context.Context, nodeName, kubeletRoot string, manager *rootfsd.Manager, capabilities CapabilityOptions) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("pod reconciler disabled: in-cluster config unavailable: %v", err)
		return
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Printf("pod reconciler disabled: create Kubernetes client: %v", err)
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if nodeName != "" && !capabilities.Disabled {
			if err := PublishCapabilityLabels(ctx, client, nodeName, capabilities); err != nil {
				log.Printf("publish rootpv node capabilities: %v", err)
			}
		}
		reconcilePods(ctx, client, nodeName, kubeletRoot, manager)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func reconcilePods(ctx context.Context, client kubernetes.Interface, nodeName, kubeletRoot string, manager *rootfsd.Manager) {
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		log.Printf("list local pods: %v", err)
		return
	}
	activeRootfsUIDs := map[string]struct{}{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if rootfsEnabled(pod) {
			activeRootfsUIDs[string(pod.UID)] = struct{}{}
		}
		podRootfs, ok, err := PodRootfsFromPod(pod, kubeletRoot)
		if err != nil {
			if rootfsEnabled(pod) {
				log.Printf("pod %s/%s rootfs not ready: %v", pod.Namespace, pod.Name, err)
			}
			continue
		}
		if !ok {
			continue
		}
		if err := manager.RegisterPod(ctx, podRootfs); err != nil {
			log.Printf("register pod %s/%s rootfs: %v", pod.Namespace, pod.Name, err)
			continue
		}
		if err := manager.PreparePod(ctx, podRootfs.PodUID); err != nil {
			log.Printf("prepare pod %s/%s rootfs: %v", pod.Namespace, pod.Name, err)
		}
	}
	manager.PrunePods(activeRootfsUIDs)
}

func PodRootfsFromPod(pod *corev1.Pod, kubeletRoot string) (rootfsd.PodRootfs, bool, error) {
	volumes := rootfs.PodPVCVolumes(pod)
	specs, enabled, err := rootfs.ParsePodSpec(pod.Annotations, rootfs.PodContainerNames(pod), volumes)
	if err != nil || !enabled {
		return rootfsd.PodRootfs{}, enabled, err
	}
	pvcHostPaths := map[string]string{}
	for _, spec := range specs {
		if pvcHostPaths[spec.VolumeName] != "" {
			continue
		}
		hostPath, err := rootfsd.DiscoverPVCHostPath(kubeletRoot, string(pod.UID), spec.VolumeName, spec.PVCName)
		if err != nil {
			return rootfsd.PodRootfs{}, true, err
		}
		pvcHostPaths[spec.VolumeName] = hostPath
	}
	return rootfsd.PodRootfs{
		Namespace:    pod.Namespace,
		PodName:      pod.Name,
		PodUID:       string(pod.UID),
		NodeName:     pod.Spec.NodeName,
		Containers:   rootfs.PodContainerNames(pod),
		PVCHostPaths: pvcHostPaths,
		Specs:        specs,
	}, true, nil
}

func rootfsEnabled(pod *corev1.Pod) bool {
	return rootfs.PersistentRootfsEnabled(pod.Annotations)
}
