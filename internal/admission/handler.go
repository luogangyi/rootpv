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
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"gitcode.com/openFuyao/rootpv/internal/config"
	"gitcode.com/openFuyao/rootpv/internal/webhook"
)

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// HandleMutatePod 将内存中的 Pod 变更编码为 JSONPatch，避免覆盖 API Server 并发写入的无关字段。
func HandleMutatePod(req *admissionv1.AdmissionRequest, cfg config.Config) *admissionv1.AdmissionResponse {
	if req == nil {
		return deny("", fmt.Errorf("admission request is nil"))
	}
	pod, err := decodePod(req)
	if err != nil {
		return deny(req.UID, err)
	}
	annotationsExisted := pod.Annotations != nil
	runtimeExisted := pod.Spec.RuntimeClassName != nil
	mutated, err := webhook.MutatePod(pod, cfg)
	if err != nil {
		return deny(req.UID, err)
	}
	resp := &admissionv1.AdmissionResponse{UID: req.UID, Allowed: true}
	if !mutated {
		return resp
	}
	ops := []patchOperation{
		{Op: addOrReplace(annotationsExisted), Path: "/metadata/annotations", Value: pod.Annotations},
		{Op: "replace", Path: "/spec/containers", Value: pod.Spec.Containers},
	}
	if pod.Spec.RuntimeClassName != nil {
		ops = append(ops, patchOperation{Op: addOrReplace(runtimeExisted), Path: "/spec/runtimeClassName", Value: *pod.Spec.RuntimeClassName})
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		return deny(req.UID, err)
	}
	patchType := admissionv1.PatchTypeJSONPatch
	resp.PatchType = &patchType
	resp.Patch = patch
	return resp
}

// HandleValidatePod 在准入链路中失败关闭：RootPV 路由证据不完整或不一致即拒绝创建。
func HandleValidatePod(req *admissionv1.AdmissionRequest, cfg config.Config) *admissionv1.AdmissionResponse {
	if req == nil {
		return deny("", fmt.Errorf("admission request is nil"))
	}
	pod, err := decodePod(req)
	if err != nil {
		return deny(req.UID, err)
	}
	if err := webhook.ValidatePod(pod, cfg); err != nil {
		return deny(req.UID, err)
	}
	return &admissionv1.AdmissionResponse{UID: req.UID, Allowed: true}
}

func decodePod(req *admissionv1.AdmissionRequest) (*corev1.Pod, error) {
	if len(req.Object.Raw) == 0 {
		return nil, fmt.Errorf("admission request object is empty")
	}
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return nil, fmt.Errorf("decode pod: %w", err)
	}
	return &pod, nil
}

func deny(uid types.UID, err error) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		UID:     uid,
		Allowed: false,
		Result: &metav1.Status{
			Status:  metav1.StatusFailure,
			Message: err.Error(),
		},
	}
}

func addOrReplace(existed bool) string {
	if existed {
		return "replace"
	}
	return "add"
}
