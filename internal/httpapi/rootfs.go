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
	"context"
	"encoding/json"
	"net/http"

	rootfsapi "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
)

type RootfsService interface {
	ResolveRootfs(context.Context, rootfsapi.ResolveRootfsRequest) (rootfsapi.ResolveRootfsResponse, error)
	MarkMounted(context.Context, rootfsapi.MarkMountedRequest) (rootfsapi.MarkMountedResponse, error)
	BeginCreate(context.Context, rootfsapi.BeginCreateRequest) (rootfsapi.BeginCreateResponse, error)
	RegisterDelegate(context.Context, rootfsapi.RegisterDelegateRequest) (rootfsapi.RegisterDelegateResponse, error)
	CommitCreate(context.Context, rootfsapi.CommitCreateRequest) (rootfsapi.CommitCreateResponse, error)
	ReleaseRootfs(context.Context, rootfsapi.ReleaseRootfsRequest) (rootfsapi.ReleaseRootfsResponse, error)
}

func NewRootfsHandler(service RootfsService) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/rootfs/resolve", rootfsRoute(service.ResolveRootfs))
	mux.Handle("/rootfs/release", rootfsRoute(service.ReleaseRootfs))
	mux.Handle("/rootfs/mounted", rootfsRoute(service.MarkMounted))
	mux.Handle("/rootfs/create/begin", rootfsRoute(service.BeginCreate))
	mux.Handle("/rootfs/create/register", rootfsRoute(service.RegisterDelegate))
	mux.Handle("/rootfs/create/commit", rootfsRoute(service.CommitCreate))
	return mux
}

// rootfsRoute 统一执行 Rootfs 生命周期接口的 POST、JSON 解码、服务调用与响应编码。
func rootfsRoute[Request any, Response any](call func(context.Context, Request) (Response, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := call(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, resp)
	})
}
