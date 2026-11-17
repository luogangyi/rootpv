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
	"errors"
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
	CRIOSocket       string
	CRIODropIn       string
	Namespace        string
	ContainerdConfig string
	HostRoot         string
	WrapperBinary    string
	RegistryPath     string
	RestartCommand   string
	SkipReadiness    bool
	// Remember successful reloads for this loop even if marker cleanup fails.
	reloadCompleted bool
}

func StartRuntimeAdapterReconciler(ctx context.Context, nodeName string, opts RuntimeAdapterReconcilerOptions) {
	if nodeName == "" || (opts.ContainerdConfig == "" && opts.CRIOSocket == "") || opts.RegistryPath == "" {
		return
	}
	if strings.TrimSpace(opts.RestartCommand) == "" {
		commandFlag := "--containerd-restart-command"
		if opts.CRIOSocket != "" {
			commandFlag = "--crio-restart-command"
		}
		log.Printf("runtime adapter reconciler disabled: set %s to activate runtime configuration changes", commandFlag)
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
	previousUnavailable := ""
	sync := func() {
		previousUnavailable = syncRuntimeAdapters(ctx, client, nodeName, &opts, previousUnavailable)
	}
	RunPeriodically(ctx, 2*time.Second, sync)
}

func (opts RuntimeAdapterReconcilerOptions) publishReadiness(ctx context.Context, client kubernetes.Interface, nodeName string, readiness map[string]string) error {
	if opts.SkipReadiness {
		return nil
	}
	return PublishAdapterReadiness(ctx, client, nodeName, readiness)
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
func syncRuntimeAdapters(ctx context.Context, client kubernetes.Interface, nodeName string, opts *RuntimeAdapterReconcilerOptions, previousUnavailable string) string {
	catalog, err := loadRuntimeCatalog(ctx, client, opts.Namespace)
	if err != nil {
		log.Printf("load runtime catalog: %v", err)
		if err := opts.publishReadiness(ctx, client, nodeName, nil); err != nil {
			log.Printf("withdraw runtime adapter readiness: %v", err)
		}
		return previousUnavailable
	}
	result, err := reconcileAndActivateAdapters(ctx, client, nodeName, opts, catalog)
	if err != nil {
		log.Printf("activate runtime adapters: %v", err)
		if err := opts.publishReadiness(ctx, client, nodeName, nil); err != nil {
			log.Printf("withdraw runtime adapter readiness: %v", err)
		}
		return previousUnavailable
	}
	if err := opts.publishReadiness(ctx, client, nodeName, result.Readiness); err != nil {
		log.Printf("publish runtime adapter readiness: %v", err)
		return previousUnavailable
	}
	unavailable := fmt.Sprint(result.Unavailable)
	if unavailable != previousUnavailable {
		for id, reason := range result.Unavailable {
			log.Printf("runtime adapter %q unavailable: %s", id, reason)
		}
	}
	return unavailable
}

// Persist reload intent before a changed file. A failed reload or interrupted
// reconcile withholds readiness and retries even when files already match.
func reconcileAndActivateAdapters(ctx context.Context, client kubernetes.Interface, nodeName string, opts *RuntimeAdapterReconcilerOptions, catalog config.RuntimeCatalog) (RuntimeReconcileResult, error) {
	if opts.CRIOSocket != "" {
		return reconcileAndActivateCRIO(ctx, client, nodeName, opts, catalog)
	}
	backup, err := os.ReadFile(opts.ContainerdConfig)
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	marker := opts.RegistryPath + ".pending-reload"
	_, err = os.Stat(marker)
	if err != nil && !os.IsNotExist(err) {
		return RuntimeReconcileResult{}, err
	}
	pending := err == nil && !opts.reloadCompleted
	if pending {
		if err := opts.publishReadiness(ctx, client, nodeName, nil); err != nil {
			return RuntimeReconcileResult{}, err
		}
	}
	beforeWrite := func() error {
		if pending {
			return nil
		}
		if err := opts.publishReadiness(ctx, client, nodeName, nil); err != nil {
			return err
		}
		if err := writeFileAtomicMode(marker, []byte("reload required\n"), 0o600); err != nil {
			return err
		}
		opts.reloadCompleted = false
		pending = true
		return nil
	}
	result, err := ReconcileRuntimeAdapters(opts.ContainerdConfig, catalog, RuntimeReconcileOptions{
		HostRoot: opts.HostRoot, WrapperBinary: opts.WrapperBinary, RegistryPath: opts.RegistryPath, beforeWrite: beforeWrite,
	})
	if err != nil {
		return RuntimeReconcileResult{}, err
	}
	if err := restartContainerd(ctx, *opts, result.Changed || pending, backup); err != nil {
		return RuntimeReconcileResult{}, err
	}
	if pending {
		opts.reloadCompleted = true
	}
	if opts.reloadCompleted {
		err := os.Remove(marker)
		opts.reloadCompleted = err != nil && !os.IsNotExist(err)
		if opts.reloadCompleted && pending {
			log.Printf("containerd reload succeeded; retrying marker cleanup without another reload: %v", err)
		}
	}
	return result, nil
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
	if !changed {
		return nil
	}
	if strings.TrimSpace(opts.RestartCommand) == "" {
		return fmt.Errorf("containerd restart or reload command is required")
	}
	if err := exec.CommandContext(ctx, "/bin/sh", "-ec", opts.RestartCommand).Run(); err != nil {
		info, statErr := os.Stat(opts.ContainerdConfig)
		if statErr != nil {
			return errors.Join(err, statErr)
		}
		return errors.Join(err, writeFileAtomicMode(opts.ContainerdConfig, backup, info.Mode().Perm()))
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
