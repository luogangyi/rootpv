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

import (
	"strings"
	"testing"
)

func TestRuntimeAdapterConfigResolvesExplicitAdapterWithStableDigest(t *testing.T) {
	cfg := RuntimeAdapterConfig{
		DefaultRuntimePolicy: DefaultRuntimePolicyFixed,
		FixedDefaultAdapter:  "runc-v1",
		Adapters: []RuntimeAdapter{
			{
				ID:                   "runc-v1",
				Mode:                 RuntimeAdapterModeExplicit,
				OriginalRuntimeClass: "runc",
				OriginalHandler:      "runc",
				RootfsRuntimeClass:   "persistent-rootfs-runc",
				RootfsHandler:        "persistent-rootfs-runc",
				RuntimeType:          RuncV2RuntimeType,
				DelegateBinary:       "/usr/bin/runc",
			},
		},
	}

	adapter, err := cfg.Resolve("runc")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adapter.ID != "runc-v1" || adapter.RootfsRuntimeClass != "persistent-rootfs-runc" {
		t.Fatalf("unexpected adapter: %+v", adapter)
	}
	first, err := adapter.ClusterDigest()
	if err != nil {
		t.Fatalf("first ClusterDigest: %v", err)
	}
	second, err := adapter.ClusterDigest()
	if err != nil {
		t.Fatalf("second ClusterDigest: %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("digest must be stable, got %q and %q", first, second)
	}
}

func TestRuntimeAdapterConfigResolvesBuiltInNodeDefault(t *testing.T) {
	cfg := RuntimeAdapterConfig{DefaultRuntimePolicy: DefaultRuntimePolicyNodeDefault}

	adapter, err := cfg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if adapter != BuiltInNodeDefaultAdapter() {
		t.Fatalf("unexpected node default adapter: %+v", adapter)
	}
	if _, err := adapter.ClusterDigest(); err != nil {
		t.Fatalf("node default digest: %v", err)
	}
}

func TestRuntimeAdapterConfigValidateAppliesNodeDefault(t *testing.T) {
	cfg := RuntimeAdapterConfig{}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.DefaultRuntimePolicy != DefaultRuntimePolicyNodeDefault {
		t.Fatalf("DefaultRuntimePolicy = %q, want %q", cfg.DefaultRuntimePolicy, DefaultRuntimePolicyNodeDefault)
	}
}

func TestRuntimeAdapterConfigRejectsInvalidAdapterDefinitions(t *testing.T) {
	valid := RuntimeAdapter{
		ID:                   "runc-v1",
		Mode:                 RuntimeAdapterModeExplicit,
		OriginalRuntimeClass: "runc",
		OriginalHandler:      "runc",
		RootfsRuntimeClass:   "persistent-rootfs-runc",
		RootfsHandler:        "persistent-rootfs-runc",
		RuntimeType:          RuncV2RuntimeType,
		DelegateBinary:       "/usr/bin/runc",
	}
	cases := []struct {
		name         string
		adapters     []RuntimeAdapter
		fixedDefault string
		want         string
	}{
		{name: "duplicate id", adapters: []RuntimeAdapter{valid, valid}, fixedDefault: valid.ID, want: "duplicate runtime adapter id"},
		{name: "reserved node default", adapters: []RuntimeAdapter{{ID: "node-default", Mode: RuntimeAdapterModeExplicit}}, fixedDefault: "node-default", want: "reserved node-default mode"},
		{name: "unsupported type", adapters: []RuntimeAdapter{{ID: "kata-v1", Mode: RuntimeAdapterModeExplicit, OriginalRuntimeClass: "kata", OriginalHandler: "kata", RootfsRuntimeClass: "persistent-rootfs-kata", RootfsHandler: "persistent-rootfs-kata", RuntimeType: "io.containerd.kata.v2", DelegateBinary: "/usr/bin/kata-runtime"}}, fixedDefault: "kata-v1", want: "unsupported runtime type"},
		{name: "too long label", adapters: []RuntimeAdapter{{ID: "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwx", Mode: RuntimeAdapterModeExplicit, OriginalRuntimeClass: "long", OriginalHandler: "long", RootfsRuntimeClass: "persistent-rootfs-long", RootfsHandler: "persistent-rootfs-long", RuntimeType: RuncV2RuntimeType, DelegateBinary: "/usr/bin/runc"}}, fixedDefault: "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwx", want: "invalid runtime adapter id"},
		{name: "fixed default not registered", adapters: []RuntimeAdapter{valid}, fixedDefault: "missing", want: `fixed default adapter "missing" is not registered`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RuntimeAdapterConfig{DefaultRuntimePolicy: DefaultRuntimePolicyFixed, FixedDefaultAdapter: tt.fixedDefault, Adapters: tt.adapters}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error = %v, want %q", err, tt.want)
			}
		})
	}
}
