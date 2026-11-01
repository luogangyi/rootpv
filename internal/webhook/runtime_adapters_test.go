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

package webhook

import (
	"testing"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
)

func TestMutatePodWritesRuntimeAdapterEvidence(t *testing.T) {
	pod := annotatedPod()
	runtime := "runc"
	pod.Spec.RuntimeClassName = &runtime

	if _, err := MutatePod(pod, adapterTestConfig()); err != nil {
		t.Fatalf("MutatePod: %v", err)
	}
	if got := *pod.Spec.RuntimeClassName; got != "persistent-rootfs-runc" {
		t.Fatalf("runtimeClassName=%q", got)
	}
	if pod.Annotations[rootfs.AnnotationRuntimeAdapterID] != "runc-v1" {
		t.Fatalf("adapter id=%q", pod.Annotations[rootfs.AnnotationRuntimeAdapterID])
	}
	if pod.Annotations[rootfs.AnnotationRuntimeAdapterMode] != string(config.RuntimeAdapterModeExplicit) {
		t.Fatalf("adapter mode=%q", pod.Annotations[rootfs.AnnotationRuntimeAdapterMode])
	}
	if pod.Annotations[rootfs.AnnotationRuntimeAdapterConfigHash] == "" {
		t.Fatal("missing adapter digest")
	}
	if err := ValidatePod(pod, adapterTestConfig()); err != nil {
		t.Fatalf("ValidatePod: %v", err)
	}
}

func TestValidatePodRejectsRuntimeClassOverwriteAfterMutation(t *testing.T) {
	pod := annotatedPod()
	if _, err := MutatePod(pod, adapterTestConfig()); err != nil {
		t.Fatalf("MutatePod: %v", err)
	}
	runtime := "runc"
	pod.Spec.RuntimeClassName = &runtime

	if err := ValidatePod(pod, adapterTestConfig()); err == nil {
		t.Fatal("expected overwritten runtimeClassName rejection")
	}
}

func TestMutatePodUsesNodeDefaultForEmptyRuntimeClass(t *testing.T) {
	pod := annotatedPod()
	if _, err := MutatePod(pod, adapterTestConfig()); err != nil {
		t.Fatalf("MutatePod: %v", err)
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "persistent-rootfs-node-default" {
		t.Fatalf("unexpected runtimeClassName: %v", pod.Spec.RuntimeClassName)
	}
	if got := pod.Annotations[rootfs.AnnotationRuntimeAdapterID]; got != "node-default" {
		t.Fatalf("adapter ID=%q", got)
	}
}

func adapterTestConfig() config.Config {
	return config.Config{
		MutationVersion:       MutationVersion,
		RuntimeAdapterCutover: true,
		RuntimeAdapters: config.RuntimeAdapterConfig{
			DefaultRuntimePolicy: config.DefaultRuntimePolicyNodeDefault,
			Adapters: []config.RuntimeAdapter{{
				ID:                   "runc-v1",
				Mode:                 config.RuntimeAdapterModeExplicit,
				OriginalRuntimeClass: "runc",
				OriginalHandler:      "runc",
				RootfsRuntimeClass:   "persistent-rootfs-runc",
				RootfsHandler:        "persistent-rootfs-runc",
				RuntimeType:          config.RuncV2RuntimeType,
				DelegateBinary:       "/usr/bin/runc",
			}},
		},
	}
}

func TestRunscCatalogMutatesOptInAndPreservesOrdinaryPod(t *testing.T) {
	catalog, err := config.BuildRuntimeCatalog([]config.RuntimeInventory{{NodeUID: "test-node", Handlers: []config.DiscoveredRuntimeHandler{{Name: "runsc", RuntimeType: config.RunscRuntimeType, Resolved: true}}}}, []config.RuntimeClassSource{{Name: "gvisor", Handler: "runsc"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RuntimeAdapterCutover, cfg.RuntimeCatalog = true, catalog
	for _, optedIn := range []bool{true, false} {
		pod := annotatedPod()
		runtime := "gvisor"
		pod.Spec.RuntimeClassName = &runtime
		if !optedIn {
			pod.Annotations = map[string]string{"example.com/owner": "workload"}
		}
		changed, err := MutatePod(pod, cfg)
		if err != nil || changed != optedIn {
			t.Fatalf("mutation: changed=%v error=%v", changed, err)
		}
		if optedIn {
			if *pod.Spec.RuntimeClassName != "persistent-rootfs-gvisor" || pod.Annotations[rootfs.AnnotationRuntimeAdapterID] != "runsc-v1" {
				t.Fatalf("routing: %#v", pod)
			}
			if err := ValidatePod(pod, cfg); err != nil {
				t.Fatal(err)
			}
		} else if *pod.Spec.RuntimeClassName != "gvisor" || len(pod.Annotations) != 1 || pod.Annotations["example.com/owner"] != "workload" {
			t.Fatal("ordinary gVisor pod mutated")
		} else {
			// The original RuntimeClass and metadata are preserved.
		}
	}
}
