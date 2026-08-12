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

package httpapi

import (
	"encoding/json"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"

	"gitcode.com/openFuyao/rootpv/internal/admission"
	"gitcode.com/openFuyao/rootpv/internal/config"
)

func NewAdmissionHandler(cfg config.Config) http.Handler {
	return NewAdmissionHandlerWithConfigProvider(func() config.Config { return cfg })
}

// NewAdmissionHandlerWithConfigProvider reads the current generated catalog
// for every admission request, allowing controller reconciliation to take
// effect without restarting the webhook server.
func NewAdmissionHandlerWithConfigProvider(provider func() config.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-v1-pod", admissionEndpoint(func(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
		return admission.HandleMutatePod(req, provider())
	}))
	mux.HandleFunc("/validate-v1-pod", admissionEndpoint(func(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
		return admission.HandleValidatePod(req, provider())
	}))
	return mux
}

func admissionEndpoint(handle func(*admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var review admissionv1.AdmissionReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := handle(review.Request)
		review.Response = resp
		review.Request = nil
		writeJSON(w, review)
	}
}
