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
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	LabelInstallIntent         = "openfuyao.cn/persistent-rootfs"
	LabelPersistentRootfsReady = "openfuyao.cn/persistent-rootfs-ready"
	defaultWrapperPath         = "/host/usr/local/bin/rootfs-runtime-wrapper"
	defaultRootfsdSocket       = "/run/persistent-rootfsd/rootfsd.sock"
)

type CapabilityOptions struct {
	WrapperPaths  []string
	RootfsdSocket string
	PathExists    func(string) bool
}

func PublishCapabilityLabels(ctx context.Context, client kubernetes.Interface, nodeName string, opts CapabilityOptions) error {
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	labels := DetectCapabilityLabels(node, opts)
	patch := map[string]any{
		"metadata": map[string]any{
			"labels": labels,
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = client.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, data, metav1.PatchOptions{})
	return err
}

func DetectCapabilityLabels(node *corev1.Node, opts CapabilityOptions) map[string]string {
	opts = opts.withDefaults()
	wrapperReady := anyPathExists(opts.PathExists, opts.WrapperPaths)
	socketReady := opts.PathExists(opts.RootfsdSocket)
	ready := wrapperReady && socketReady
	return map[string]string{
		LabelPersistentRootfsReady: boolLabel(ready),
	}
}

func (opts CapabilityOptions) withDefaults() CapabilityOptions {
	if opts.PathExists == nil {
		opts.PathExists = pathExists
	}
	if len(opts.WrapperPaths) == 0 {
		opts.WrapperPaths = []string{defaultWrapperPath}
	}
	if opts.RootfsdSocket == "" {
		opts.RootfsdSocket = defaultRootfsdSocket
	}
	return opts
}

func anyPathExists(pathExists func(string) bool, paths []string) bool {
	for _, path := range paths {
		if pathExists(path) {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
