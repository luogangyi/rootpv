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

package admission

import (
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/rootfs"
	"gitcode.com/openFuyao/rootpv/internal/testutil"
)

func TestHandleMutatePodAllowsAndReturnsPatch(t *testing.T) {
	pod := testAdmissionPod()
	review := admissionReviewForPod(t, pod)

	resp := HandleMutatePod(review.Request, config.Default())
	if resp == nil {
		t.Fatalf("expected response")
	}
	if !resp.Allowed {
		t.Fatalf("expected allowed response: %+v", resp.Result)
	}
	if resp.PatchType == nil || *resp.PatchType != admissionv1.PatchTypeJSONPatch {
		t.Fatalf("expected JSONPatch response")
	}
	if len(resp.Patch) == 0 {
		t.Fatalf("expected non-empty patch")
	}

	var ops []patchOperation
	if err := json.Unmarshal(resp.Patch, &ops); err != nil {
		t.Fatalf("patch should be json patch: %v", err)
	}
	if len(ops) == 0 {
		t.Fatalf("expected patch operations")
	}
}

func TestHandleMutatePodAllowsPreserveModeWithoutRuntimeClass(t *testing.T) {
	pod := testAdmissionPod()
	review := admissionReviewForPod(t, pod)
	cfg := config.Default()
	cfg.RuntimeClassMode = config.RuntimeClassModePreserve

	resp := HandleMutatePod(review.Request, cfg)
	if resp == nil {
		t.Fatalf("expected response")
	}
	if !resp.Allowed {
		t.Fatalf("expected allowed response: %+v", resp.Result)
	}
	var ops []patchOperation
	if err := json.Unmarshal(resp.Patch, &ops); err != nil {
		t.Fatalf("patch should be json patch: %v", err)
	}
	for _, op := range ops {
		if op.Path == "/spec/runtimeClassName" {
			t.Fatalf("preserve mode should not patch runtimeClassName when it is empty: %+v", ops)
		}
	}
}

func TestHandleValidatePodRejectsInvalidPod(t *testing.T) {
	pod := testAdmissionPod()
	pod.Annotations[rootfs.AnnotationVolume] = "missing"
	review := admissionReviewForPod(t, pod)

	resp := HandleValidatePod(review.Request, config.Default())
	if resp == nil {
		t.Fatalf("expected response")
	}
	if resp.Allowed {
		t.Fatalf("expected rejection")
	}
	if resp.Result == nil || resp.Result.Message == "" {
		t.Fatalf("expected rejection message")
	}
}

func admissionReviewForPod(t *testing.T, pod *corev1.Pod) admissionv1.AdmissionReview {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID: "test-uid",
			Kind: metav1.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Pod",
			},
			Object: runtime.RawExtension{Raw: raw},
		},
	}
}

func testAdmissionPod() *corev1.Pod {
	pod := testutil.PersistentRootfsPod()
	pod.Spec.Containers[0].Image = "nvidia/cuda:12.4.1-base-ubuntu22.04"
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): resourceapi.MustParse("1")}
	return pod
}
