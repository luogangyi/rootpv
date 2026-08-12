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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDetectCapabilityLabelsUsesOnlyGenericRootPVReadiness(t *testing.T) {
	node := &corev1.Node{}
	paths := map[string]bool{
		"/host/usr/local/bin/rootfs-runtime-wrapper": true,
		"/run/persistent-rootfsd/rootfsd.sock":       true,
	}

	labels := DetectCapabilityLabels(node, CapabilityOptions{
		PathExists: func(path string) bool { return paths[path] },
	})

	if labels[LabelPersistentRootfsReady] != "true" {
		t.Fatalf("expected ready label, got %q", labels[LabelPersistentRootfsReady])
	}
	if len(labels) != 1 {
		t.Fatalf("capabilities must not have vendor-specific labels: %#v", labels)
	}
}

func TestDetectCapabilityLabelsRequiresWrapperAndRootfsd(t *testing.T) {
	paths := map[string]bool{
		"/host/usr/local/bin/rootfs-runtime-wrapper": true,
		"/run/persistent-rootfsd/rootfsd.sock":       true,
	}

	labels := DetectCapabilityLabels(&corev1.Node{}, CapabilityOptions{
		PathExists: func(path string) bool { return paths[path] },
	})

	if labels[LabelPersistentRootfsReady] != "true" {
		t.Fatalf("expected generic readiness, got %#v", labels)
	}
}

func TestPublishCapabilityLabelsPatchesNode(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-a",
			Labels: map[string]string{LabelInstallIntent: "true"},
		},
	})
	paths := map[string]bool{
		"/host/usr/local/bin/rootfs-runtime-wrapper": true,
		"/run/persistent-rootfsd/rootfsd.sock":       true,
	}

	if err := PublishCapabilityLabels(context.Background(), client, "node-a", CapabilityOptions{
		PathExists: func(path string) bool { return paths[path] },
	}); err != nil {
		t.Fatalf("PublishCapabilityLabels: %v", err)
	}

	node, err := client.CoreV1().Nodes().Get(context.Background(), "node-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Labels[LabelInstallIntent] != "true" {
		t.Fatalf("must preserve non-owned labels")
	}
	if node.Labels[LabelPersistentRootfsReady] != "true" || len(node.Labels) != 2 {
		t.Fatalf("unexpected labels: %#v", node.Labels)
	}
}
