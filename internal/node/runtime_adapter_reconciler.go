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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

type RuntimeAdapterReconcilerOptions struct {
	Namespace        string
	ContainerdConfig string
	HostRoot         string
	WrapperBinary    string
	RegistryPath     string
	RestartCommand   string
}

func StartRuntimeAdapterReconciler(ctx context.Context, nodeName string, opts RuntimeAdapterReconcilerOptions) {
	if nodeName == "" || opts.ContainerdConfig == "" || opts.RegistryPath == "" {
		return
	}
	if opts.Namespace == "" {
		opts.Namespace = "rootpv-system"
	}
	client, err := inClusterClient()
	if err != nil {
		log.Printf("runtime adapter reconciler disabled: in-cluster config unavailable: %v", err)
		return
	}
	sync := func() {
		syncRuntimeAdapters(ctx, client, nodeName, opts)
	}
	RunPeriodically(ctx, 2*time.Second, sync)
}

// inClusterClient 创建组件共用的 Kubernetes 客户端；调用者负责记录与自身语义相关的错误信息。
func inClusterClient() (kubernetes.Interface, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}

// syncRuntimeAdapters 根据控制面下发的目录生成本节点的 shadow handler，并在配置生效后发布就绪状态。
func syncRuntimeAdapters(ctx context.Context, client kubernetes.Interface, nodeName string, opts RuntimeAdapterReconcilerOptions) {
	catalog, err := loadRuntimeCatalog(ctx, client, opts.Namespace)
	if err != nil {
		log.Printf("load runtime catalog: %v", err)
		return
	}
	backup, err := os.ReadFile(opts.ContainerdConfig)
	if err != nil {
		log.Printf("read containerd config before adapter reconcile: %v", err)
		return
	}
	result, err := ReconcileRuntimeAdapters(opts.ContainerdConfig, catalog, RuntimeReconcileOptions{HostRoot: opts.HostRoot, WrapperBinary: opts.WrapperBinary, RegistryPath: opts.RegistryPath})
	if err != nil {
		log.Printf("reconcile node runtime adapters: %v", err)
		return
	}
	if err := restartContainerd(ctx, opts, result.Changed, backup); err != nil {
		log.Printf("restart containerd after adapter reconcile failed; restored previous config: %v", err)
		return
	}
	if err := PublishAdapterReadiness(ctx, client, nodeName, result.Readiness); err != nil {
		log.Printf("publish runtime adapter readiness: %v", err)
	}
}

func loadRuntimeCatalog(ctx context.Context, client kubernetes.Interface, namespace string) (config.RuntimeCatalog, error) {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, config.RuntimeCatalogConfigMapName, metav1.GetOptions{})
	if err != nil {
		return config.RuntimeCatalog{}, err
	}
	var catalog config.RuntimeCatalog
	if err := yaml.Unmarshal([]byte(cm.Data[config.RuntimeCatalogDataKey]), &catalog); err != nil {
		return config.RuntimeCatalog{}, fmt.Errorf("decode runtime catalog: %w", err)
	}
	return catalog, nil
}

// restartContainerd 只在配置发生变化时重启 containerd；重启失败会立即恢复变更前的配置。
func restartContainerd(ctx context.Context, opts RuntimeAdapterReconcilerOptions, changed bool, backup []byte) error {
	if !changed || opts.RestartCommand == "" {
		return nil
	}
	if err := exec.CommandContext(ctx, "/bin/sh", "-ec", opts.RestartCommand).Run(); err != nil {
		_ = writeFileAtomicMode(opts.ContainerdConfig, backup, 0o644)
		return err
	}
	return nil
}

func PublishAdapterReadiness(ctx context.Context, client kubernetes.Interface, nodeName string, readiness map[string]string) error {
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	labels := map[string]any{}
	for key := range node.Labels {
		if strings.HasPrefix(key, "openfuyao.cn/rootpv-adapter-") {
			labels[key] = nil
		}
	}
	for adapterID, value := range readiness {
		if adapterID == "" || value == "" {
			continue
		}
		labels["openfuyao.cn/rootpv-adapter-"+adapterID] = value
	}
	payload, err := json.Marshal(map[string]any{"metadata": map[string]any{"labels": labels}})
	if err != nil {
		return err
	}
	_, err = client.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, payload, metav1.PatchOptions{})
	return err
}

func ValidateRuntimeCatalog(catalog config.RuntimeCatalog) error {
	if catalog.FormatVersion != config.RuntimeCatalogFormatVersion {
		return fmt.Errorf("unsupported runtime catalog format %q", catalog.FormatVersion)
	}
	return nil
}
