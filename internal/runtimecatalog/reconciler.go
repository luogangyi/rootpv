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
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

type Reconciler struct {
	Client    kubernetes.Interface
	Namespace string
}

// Reconcile 汇总所有节点发布的运行时清单和用户 RuntimeClass，生成跨节点可用的 adapter 目录，
// 再发布 ConfigMap 与对应的 shadow RuntimeClass，使 admission 和节点配置使用同一份路由事实。
func (r Reconciler) Reconcile(ctx context.Context) (config.RuntimeCatalog, error) {
	if r.Client == nil {
		return config.RuntimeCatalog{}, fmt.Errorf("runtime catalog Kubernetes client is required")
	}
	if r.Namespace == "" {
		return config.RuntimeCatalog{}, fmt.Errorf("runtime catalog namespace is required")
	}
	nodes, err := r.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return config.RuntimeCatalog{}, err
	}
	classes, err := r.Client.NodeV1().RuntimeClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return config.RuntimeCatalog{}, err
	}
	catalog, err := config.BuildRuntimeCatalog(inventoriesFromNodes(nodes.Items), runtimeClassSources(classes.Items))
	if err != nil {
		return config.RuntimeCatalog{}, err
	}
	if err := r.publishRuntimeResources(ctx, &catalog, classes.Items); err != nil {
		return config.RuntimeCatalog{}, err
	}
	return catalog, nil
}

// publishRuntimeResources 先发布目录，再同步其派生的 RuntimeClass，保证节点与 admission 读取到同一代路由。
func (r Reconciler) publishRuntimeResources(ctx context.Context, catalog *config.RuntimeCatalog, classes []nodev1.RuntimeClass) error {
	if err := r.publishCatalog(ctx, catalog); err != nil {
		return err
	}
	return r.reconcileShadowRuntimeClasses(ctx, *catalog, classes)
}

func inventoriesFromNodes(nodes []corev1.Node) []config.RuntimeInventory {
	var inventories []config.RuntimeInventory
	for _, node := range nodes {
		annotations := node.Annotations
		if annotations[config.RuntimeInventoryVersionAnnotation] != config.RuntimeInventoryFormatVersion {
			continue
		}
		var inventory config.RuntimeInventory
		if err := json.Unmarshal([]byte(annotations[config.RuntimeInventoryAnnotation]), &inventory); err != nil {
			continue
		}
		if inventory.NodeUID == "" || inventory.NodeUID != string(node.UID) {
			continue
		}
		inventories = append(inventories, inventory)
	}
	return inventories
}

func runtimeClassSources(classes []nodev1.RuntimeClass) []config.RuntimeClassSource {
	sources := make([]config.RuntimeClassSource, 0, len(classes))
	for _, class := range classes {
		if class.Labels[config.RuntimeCatalogManagedByLabel] == config.RuntimeCatalogManagedByValue || config.IsRootPVShadowHandler(class.Handler) {
			continue
		}
		sources = append(sources, config.RuntimeClassSource{
			Name:     class.Name,
			Handler:  class.Handler,
			OptedOut: class.Annotations[config.RuntimeClassOptOutAnnotation] == "disabled",
		})
	}
	return sources
}

// publishCatalog 仅在目录内容变化时递增 generation；节点据此识别是否需要重新生成本地配置。
func (r Reconciler) publishCatalog(ctx context.Context, catalog *config.RuntimeCatalog) error {
	existing, getErr := r.Client.CoreV1().ConfigMaps(r.Namespace).Get(ctx, config.RuntimeCatalogConfigMapName, metav1.GetOptions{})
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return getErr
	}
	setCatalogGeneration(catalog, existing, getErr)
	data, err := yaml.Marshal(catalog)
	if err != nil {
		return err
	}
	if apierrors.IsNotFound(getErr) {
		return r.createCatalog(ctx, data)
	}
	return r.updateCatalog(ctx, existing, data)
}

// setCatalogGeneration 仅在有效目录发生变化时递增 generation；无法解码的旧目录视为首代目录。
func setCatalogGeneration(catalog *config.RuntimeCatalog, existing *corev1.ConfigMap, getErr error) {
	if getErr != nil {
		catalog.Generation = 1
		return
	}
	var previous config.RuntimeCatalog
	if yaml.Unmarshal([]byte(existing.Data[config.RuntimeCatalogDataKey]), &previous) != nil {
		catalog.Generation = 1
		return
	}
	catalog.Generation = previous.Generation + 1
	if sameCatalog(previous, *catalog) {
		catalog.Generation = previous.Generation
	}
	if catalog.Generation == 0 {
		catalog.Generation = 1
	}
}

func (r Reconciler) createCatalog(ctx context.Context, data []byte) error {
	_, err := r.Client.CoreV1().ConfigMaps(r.Namespace).Create(ctx, newRuntimeCatalogConfigMap(data), metav1.CreateOptions{})
	return err
}

func (r Reconciler) updateCatalog(ctx context.Context, existing *corev1.ConfigMap, data []byte) error {
	existing.Labels = mergeStringMap(existing.Labels, map[string]string{config.RuntimeCatalogManagedByLabel: config.RuntimeCatalogManagedByValue})
	existing.Annotations = mergeStringMap(existing.Annotations, map[string]string{"openfuyao.cn/generated": "true"})
	existing.Data = map[string]string{config.RuntimeCatalogDataKey: string(data)}
	_, err := r.Client.CoreV1().ConfigMaps(r.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func newRuntimeCatalogConfigMap(data []byte) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: config.RuntimeCatalogConfigMapName, Labels: map[string]string{config.RuntimeCatalogManagedByLabel: config.RuntimeCatalogManagedByValue}, Annotations: map[string]string{"openfuyao.cn/generated": "true"}},
		Data:       map[string]string{config.RuntimeCatalogDataKey: string(data)},
	}
}

func (r Reconciler) reconcileShadowRuntimeClasses(ctx context.Context, catalog config.RuntimeCatalog, sources []nodev1.RuntimeClass) error {
	byName := runtimeClassByName(sources)
	adapters := adaptersByID(catalog.Adapters)
	for _, route := range catalog.Routes {
		adapter, ok := adapters[route.AdapterID]
		if !ok {
			return fmt.Errorf("catalog route %q references unknown adapter %q", route.OriginalRuntimeClass, route.AdapterID)
		}
		if err := r.applyShadowRuntimeClass(ctx, desiredShadowRuntimeClass(route, adapter, byName[route.OriginalRuntimeClass])); err != nil {
			return err
		}
	}
	return nil
}

func runtimeClassByName(sources []nodev1.RuntimeClass) map[string]nodev1.RuntimeClass {
	byName := make(map[string]nodev1.RuntimeClass, len(sources))
	for _, source := range sources {
		byName[source.Name] = source
	}
	return byName
}

func adaptersByID(adapters []config.CatalogRuntimeAdapter) map[string]config.CatalogRuntimeAdapter {
	byID := make(map[string]config.CatalogRuntimeAdapter, len(adapters))
	for _, adapter := range adapters {
		byID[adapter.ID] = adapter
	}
	return byID
}

// applyShadowRuntimeClass 创建或覆盖由当前目录派生的 RuntimeClass，保留 API 要求的 ResourceVersion。
func (r Reconciler) applyShadowRuntimeClass(ctx context.Context, shadow *nodev1.RuntimeClass) error {
	existing, err := r.Client.NodeV1().RuntimeClasses().Get(ctx, shadow.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = r.Client.NodeV1().RuntimeClasses().Create(ctx, shadow, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	shadow.ResourceVersion = existing.ResourceVersion
	_, err = r.Client.NodeV1().RuntimeClasses().Update(ctx, shadow, metav1.UpdateOptions{})
	return err
}

func desiredShadowRuntimeClass(route config.RuntimeAdapterRoute, adapter config.CatalogRuntimeAdapter, source nodev1.RuntimeClass) *nodev1.RuntimeClass {
	labels := mergeStringMap(source.Labels, map[string]string{
		config.RuntimeCatalogManagedByLabel: config.RuntimeCatalogManagedByValue,
		config.RuntimeAdapterIDLabel:        adapter.ID,
	})
	annotations := mergeStringMap(source.Annotations, map[string]string{"openfuyao.cn/generated": "true"})
	shadow := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: route.RootfsRuntimeClass, Labels: labels, Annotations: annotations},
		Handler:    adapter.RootfsHandler,
	}
	if source.Overhead != nil {
		shadow.Overhead = source.Overhead.DeepCopy()
	}
	if source.Scheduling != nil {
		shadow.Scheduling = source.Scheduling.DeepCopy()
	} else {
		shadow.Scheduling = &nodev1.Scheduling{}
	}
	shadow.Scheduling.NodeSelector = mergeStringMap(shadow.Scheduling.NodeSelector, map[string]string{AdapterReadinessLabel(adapter.ID): AdapterReadinessValue(adapter.ClusterDigest)})
	return shadow
}

func AdapterReadinessLabel(adapterID string) string {
	return "openfuyao.cn/rootpv-adapter-" + adapterID
}

func AdapterReadinessValue(digest string) string {
	value := strings.TrimPrefix(digest, "sha256:")
	if len(value) > 16 {
		return value[:16]
	}
	return value
}

func sameCatalog(a, b config.RuntimeCatalog) bool {
	// YAML encodes nil slices as []; both represent the same empty catalog.
	return a.FormatVersion == b.FormatVersion &&
		slices.Equal(a.Adapters, b.Adapters) && slices.Equal(a.Routes, b.Routes)
}

func mergeStringMap(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}
