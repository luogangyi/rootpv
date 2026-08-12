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

	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// StartRuntimeInventoryPublisher continuously publishes the configured CRI
// runtime facts. Failures leave the previous inventory in place; controller
// reconciliation validates the node UID and will fail closed on stale data.
func StartRuntimeInventoryPublisher(ctx context.Context, nodeName, containerdConfig, hostRoot string) {
	if nodeName == "" || containerdConfig == "" {
		return
	}
	client, err := inClusterClient()
	if err != nil {
		log.Printf("runtime inventory publisher disabled: in-cluster config unavailable: %v", err)
		return
	}
	publish := func() {
		publishRuntimeInventory(ctx, client, nodeName, containerdConfig, hostRoot)
	}
	publish()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()
}

// publishRuntimeInventory 使用本节点 UID 生成库存记录，避免控制器误用其他节点发布的数据。
func publishRuntimeInventory(ctx context.Context, client kubernetes.Interface, nodeName, containerdConfig, hostRoot string) {
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, v1.GetOptions{})
	if err != nil {
		log.Printf("get node for runtime inventory: %v", err)
		return
	}
	inventory, err := DiscoverRuntimeInventory(containerdConfig, RuntimeDiscoveryOptions{NodeUID: string(node.UID), HostRoot: hostRoot})
	if err != nil {
		log.Printf("discover node runtime inventory: %v", err)
		return
	}
	if err := PublishRuntimeInventory(ctx, client, nodeName, inventory); err != nil {
		log.Printf("publish node runtime inventory: %v", err)
	}
}
