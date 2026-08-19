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

package runtimewrapper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	apiv1alpha1 "gitcode.com/openFuyao/rootpv/internal/apis/rootfs/v1alpha1"
)

type Resolver interface {
	ResolveRootfs(context.Context, apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error)
}

type RootfsClient interface {
	Resolver
	MarkMounted(context.Context, apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error)
	BeginCreate(context.Context, apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error)
	RegisterDelegate(context.Context, apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error)
	CommitCreate(context.Context, apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error)
	ReleaseRootfs(context.Context, apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error)
}

type UnixResolver struct {
	client *http.Client
}

type RootfsdError struct {
	Path       string
	Status     string
	StatusCode int
	Body       string
}

func (e RootfsdError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("rootfsd request %s failed: %s", e.Path, e.Status)
	}
	return fmt.Sprintf("rootfsd request %s failed: %s: %s", e.Path, e.Status, body)
}

func NewUnixResolver(socket string) *UnixResolver {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &UnixResolver{
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

func (r *UnixResolver) ResolveRootfs(ctx context.Context, req apiv1alpha1.ResolveRootfsRequest) (apiv1alpha1.ResolveRootfsResponse, error) {
	var out apiv1alpha1.ResolveRootfsResponse
	if err := r.post(ctx, "/rootfs/resolve", req, &out); err != nil {
		return apiv1alpha1.ResolveRootfsResponse{}, err
	}
	return out, nil
}

func (r *UnixResolver) ReleaseRootfs(ctx context.Context, req apiv1alpha1.ReleaseRootfsRequest) (apiv1alpha1.ReleaseRootfsResponse, error) {
	var out apiv1alpha1.ReleaseRootfsResponse
	if err := r.post(ctx, "/rootfs/release", req, &out); err != nil {
		return apiv1alpha1.ReleaseRootfsResponse{}, err
	}
	return out, nil
}

func (r *UnixResolver) MarkMounted(ctx context.Context, req apiv1alpha1.MarkMountedRequest) (apiv1alpha1.MarkMountedResponse, error) {
	var out apiv1alpha1.MarkMountedResponse
	if err := r.post(ctx, "/rootfs/mounted", req, &out); err != nil {
		return apiv1alpha1.MarkMountedResponse{}, err
	}
	return out, nil
}

func (r *UnixResolver) BeginCreate(ctx context.Context, req apiv1alpha1.BeginCreateRequest) (apiv1alpha1.BeginCreateResponse, error) {
	var out apiv1alpha1.BeginCreateResponse
	if err := r.post(ctx, "/rootfs/create/begin", req, &out); err != nil {
		return apiv1alpha1.BeginCreateResponse{}, err
	}
	return out, nil
}

func (r *UnixResolver) RegisterDelegate(ctx context.Context, req apiv1alpha1.RegisterDelegateRequest) (apiv1alpha1.RegisterDelegateResponse, error) {
	var out apiv1alpha1.RegisterDelegateResponse
	if err := r.post(ctx, "/rootfs/create/register", req, &out); err != nil {
		return apiv1alpha1.RegisterDelegateResponse{}, err
	}
	return out, nil
}

func (r *UnixResolver) CommitCreate(ctx context.Context, req apiv1alpha1.CommitCreateRequest) (apiv1alpha1.CommitCreateResponse, error) {
	var out apiv1alpha1.CommitCreateResponse
	if err := r.post(ctx, "/rootfs/create/commit", req, &out); err != nil {
		return apiv1alpha1.CommitCreateResponse{}, err
	}
	return out, nil
}

func (r *UnixResolver) post(ctx context.Context, path string, req any, out any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://rootfsd"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return fmt.Errorf("read rootfsd error response: %w", err)
		}
		return RootfsdError{Path: path, Status: resp.Status, StatusCode: resp.StatusCode, Body: string(body)}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}
