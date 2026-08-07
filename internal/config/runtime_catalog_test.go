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

package config

import "testing"

func TestRuntimeAdapterIDIsDeterministicAndVendorNeutral(t *testing.T) {
	tests := []struct {
		handler string
		want    string
	}{
		{handler: "runc", want: "runc-v2"},
		{handler: "ascend", want: "ascend-runc-v2"},
		{handler: "Vendor.Example.com/Ascend_910", want: "vendor-example-com-ascend-910-runc-v2"},
	}
	for _, tt := range tests {
		got, err := RuntimeAdapterID(tt.handler, RuncV2RuntimeType)
		if err != nil {
			t.Fatalf("RuntimeAdapterID(%q): %v", tt.handler, err)
		}
		if got != tt.want {
			t.Fatalf("RuntimeAdapterID(%q)=%q, want %q", tt.handler, got, tt.want)
		}
	}
}

func TestBuildRuntimeCatalogGeneratesRoutesFromRuntimeClasses(t *testing.T) {
	catalog, err := BuildRuntimeCatalog([]RuntimeInventory{{
		NodeUID: "node-a",
		Handlers: []DiscoveredRuntimeHandler{
			{Name: "runc", RuntimeType: RuncV2RuntimeType, AdapterID: "runc-v2", Resolved: true, NodeResolvedDigest: "sha256:runc"},
			{Name: "ascend", RuntimeType: RuncV2RuntimeType, AdapterID: "ascend-runc-v2", Resolved: true, NodeResolvedDigest: "sha256:ascend"},
		},
	}}, []RuntimeClassSource{
		{Name: "ascend", Handler: "ascend"},
		{Name: "ascend-training", Handler: "ascend"},
	})
	if err != nil {
		t.Fatalf("BuildRuntimeCatalog: %v", err)
	}
	if len(catalog.Adapters) != 1 || catalog.Adapters[0].ID != "ascend-runc-v2" {
		t.Fatalf("unexpected adapters: %#v", catalog.Adapters)
	}
	if len(catalog.Routes) != 2 {
		t.Fatalf("unexpected routes: %#v", catalog.Routes)
	}
	wantRoutes := map[string]struct{}{"persistent-rootfs-ascend": {}, "persistent-rootfs-ascend-training": {}}
	for _, route := range catalog.Routes {
		if _, ok := wantRoutes[route.RootfsRuntimeClass]; !ok {
			t.Fatalf("unexpected runtime route: %#v", route)
		}
		delete(wantRoutes, route.RootfsRuntimeClass)
	}
	if len(wantRoutes) != 0 {
		t.Fatalf("missing expected runtime routes: %#v", wantRoutes)
	}
}

func TestBuildRuntimeCatalogFailsClosedForUnresolvedAndUnsupportedHandlers(t *testing.T) {
	catalog, err := BuildRuntimeCatalog([]RuntimeInventory{{
		NodeUID: "node-a",
		Handlers: []DiscoveredRuntimeHandler{
			{Name: "ascend", RuntimeType: RuncV2RuntimeType, AdapterID: "ascend-runc-v2", Resolved: false, Reason: "delegate_missing"},
			{Name: "kata", RuntimeType: "io.containerd.kata.v2", Resolved: true, Reason: "unsupported_runtime_type"},
		},
	}}, []RuntimeClassSource{{Name: "ascend", Handler: "ascend"}, {Name: "kata", Handler: "kata"}})
	if err != nil {
		t.Fatalf("BuildRuntimeCatalog: %v", err)
	}
	if len(catalog.Adapters) != 0 || len(catalog.Routes) != 0 {
		t.Fatalf("unresolved and unsupported handlers must not become routes: %#v", catalog)
	}
}

func TestBuildRuntimeCatalogRejectsRuntimeTypeConflictAcrossNodes(t *testing.T) {
	_, err := BuildRuntimeCatalog([]RuntimeInventory{
		{NodeUID: "node-a", Handlers: []DiscoveredRuntimeHandler{{Name: "ascend", RuntimeType: RuncV2RuntimeType, AdapterID: "ascend-runc-v2", Resolved: true}}},
		{NodeUID: "node-b", Handlers: []DiscoveredRuntimeHandler{{Name: "ascend", RuntimeType: "io.containerd.kata.v2", Resolved: true}}},
	}, []RuntimeClassSource{{Name: "ascend", Handler: "ascend"}})
	if err == nil {
		t.Fatal("expected runtime type conflict to fail closed")
	}
}
