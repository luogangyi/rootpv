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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"gitcode.com/openFuyao/rootpv/internal/config"
)

const maxRuntimeInventoryAnnotationBytes = 32 * 1024

func PublishRuntimeInventory(ctx context.Context, client kubernetes.Interface, nodeName string, inventory config.RuntimeInventory) error {
	if inventory.NodeUID == "" {
		return fmt.Errorf("runtime inventory node UID is required")
	}
	data, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	if len(data) > maxRuntimeInventoryAnnotationBytes {
		return fmt.Errorf("runtime inventory exceeds annotation limit %d", maxRuntimeInventoryAnnotationBytes)
	}
	patch := map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		config.RuntimeInventoryVersionAnnotation: config.RuntimeInventoryFormatVersion,
		config.RuntimeInventoryAnnotation:        string(data),
	}}}
	payload, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = client.CoreV1().Nodes().Patch(ctx, nodeName, types.MergePatchType, payload, metav1.PatchOptions{})
	return err
}
