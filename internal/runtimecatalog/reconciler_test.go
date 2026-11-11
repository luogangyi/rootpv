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

package runtimecatalog

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

func TestReconcileGeneratesCatalogAndShadowRuntimeClass(t *testing.T) {
	client := fake.NewSimpleClientset(runtimeInventoryNode(t, "node-uid"), runtimeClass("ascend"))
	reconciler := Reconciler{Client: client, Namespace: "rootpv-system"}
	catalog, err := reconciler.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	assertCatalog(t, catalog)
	assertCatalogConfigMap(t, client)
	assertShadowRuntimeClass(t, client)
}

func runtimeInventoryNode(t *testing.T, uid string) *corev1.Node {
	t.Helper()
	inventory, err := json.Marshal(config.RuntimeInventory{NodeUID: uid, Handlers: []config.DiscoveredRuntimeHandler{{Name: "ascend", RuntimeType: config.RuncV2RuntimeType, AdapterID: "ascend-runc-v2", Resolved: true, NodeResolvedDigest: "sha256:node"}}})
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: types.UID(uid), Annotations: map[string]string{config.RuntimeInventoryVersionAnnotation: config.RuntimeInventoryFormatVersion, config.RuntimeInventoryAnnotation: string(inventory)}}}
}

func runtimeClass(name string) *nodev1.RuntimeClass {
	return &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Handler: name, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"huawei.com/zone": "a"}}}
}

func assertCatalog(t *testing.T, catalog config.RuntimeCatalog) {
	t.Helper()
	if len(catalog.Adapters) != 1 || len(catalog.Routes) != 1 {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
}

func assertCatalogConfigMap(t *testing.T, client *fake.Clientset) {
	t.Helper()
	cm, err := client.CoreV1().ConfigMaps("rootpv-system").Get(context.Background(), config.RuntimeCatalogConfigMapName, metav1.GetOptions{})
	if err != nil || cm.Data[config.RuntimeCatalogDataKey] == "" {
		t.Fatalf("generated catalog ConfigMap missing: cm=%#v err=%v", cm, err)
	}
}

func assertShadowRuntimeClass(t *testing.T, client *fake.Clientset) {
	t.Helper()
	shadow, err := client.NodeV1().RuntimeClasses().Get(context.Background(), "persistent-rootfs-ascend", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Shadow RuntimeClass: %v", err)
	}
	if shadow.Handler != "persistent-rootfs-ascend-runc-v2" || shadow.Scheduling.NodeSelector["huawei.com/zone"] != "a" {
		t.Fatalf("source RuntimeClass properties not preserved: %#v", shadow)
	}
	if shadow.Scheduling.NodeSelector[AdapterReadinessLabel("ascend-runc-v2")] == "" {
		t.Fatalf("adapter readiness selector missing: %#v", shadow.Scheduling.NodeSelector)
	}
}

func TestReconcileSkipsStaleInventory(t *testing.T) {
	inventory, err := json.Marshal(config.RuntimeInventory{NodeUID: "old-uid", Handlers: []config.DiscoveredRuntimeHandler{{Name: "ascend", RuntimeType: config.RuncV2RuntimeType, Resolved: true}}})
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: types.UID("new-uid"), Annotations: map[string]string{config.RuntimeInventoryVersionAnnotation: config.RuntimeInventoryFormatVersion, config.RuntimeInventoryAnnotation: string(inventory)}}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "ascend"}, Handler: "ascend"},
	)
	catalog, err := (Reconciler{Client: client, Namespace: "rootpv-system"}).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(catalog.Adapters) != 0 || len(catalog.Routes) != 0 {
		t.Fatalf("stale inventory must not generate catalog: %#v", catalog)
	}
	// An empty catalog must remain stable after its ConfigMap YAML round trip.
	again, err := (Reconciler{Client: client, Namespace: "rootpv-system"}).Reconcile(context.Background())
	if err != nil || again.Generation != catalog.Generation {
		t.Fatalf("empty catalog triggered another reload: generation=%d err=%v", again.Generation, err)
	}
}

func TestReconcileSkipsOptedOutRuntimeClass(t *testing.T) {
	client := fake.NewSimpleClientset(
		runtimeInventoryNode(t, "node-uid"),
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "ascend", Annotations: map[string]string{config.RuntimeClassOptOutAnnotation: "disabled"}}, Handler: "ascend"},
	)
	catalog, err := (Reconciler{Client: client, Namespace: "rootpv-system"}).Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(catalog.Routes) != 0 {
		t.Fatalf("opted-out class must not generate route: %#v", catalog)
	}
}
