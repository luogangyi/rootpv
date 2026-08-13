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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

func TestPublishRuntimeInventoryPatchesOnlyRootPVOwnedAnnotations(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        "node-a",
		UID:         types.UID("node-uid"),
		Annotations: map[string]string{"example.com/keep": "yes"},
	}})
	inventory := config.RuntimeInventory{NodeUID: "node-uid", Handlers: []config.DiscoveredRuntimeHandler{{Name: "ascend", RuntimeType: config.RuncV2RuntimeType, AdapterID: "ascend-runc-v2", Resolved: true}}}
	if err := PublishRuntimeInventory(context.Background(), client, "node-a", inventory); err != nil {
		t.Fatalf("PublishRuntimeInventory: %v", err)
	}
	node, err := client.CoreV1().Nodes().Get(context.Background(), "node-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if node.Annotations["example.com/keep"] != "yes" {
		t.Fatalf("unowned annotation lost: %#v", node.Annotations)
	}
	if node.Annotations[config.RuntimeInventoryVersionAnnotation] != config.RuntimeInventoryFormatVersion || node.Annotations[config.RuntimeInventoryAnnotation] == "" {
		t.Fatalf("inventory annotations missing: %#v", node.Annotations)
	}
}
